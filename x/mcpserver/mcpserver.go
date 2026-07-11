package mcpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/version"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

const schemaResourceURI = "sql-mcp://schema"

var errInternal = errors.New("sql-mcp-server: internal operation failed")

type appAcquire func() (*bootstrap.App, func(), error)

// NewServer builds an mcp.Server with the app's enabled tools registered.
func NewServer(app *bootstrap.App) *mcp.Server {
	return newServer(func() (*bootstrap.App, func(), error) {
		return app, func() {}, nil
	})
}

// NewRuntimeServer builds a server whose handlers acquire the current app
// snapshot for every request. Tool discovery reflects the snapshot at server
// creation; request execution and resources always use the latest snapshot.
func NewRuntimeServer(runtime *bootstrap.Runtime) *mcp.Server {
	return newServer(runtime.Acquire)
}

func newServer(acquire appAcquire) *mcp.Server {
	app, release, err := acquire()
	if err != nil {
		panic(err)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "sql-mcp-server", Version: version.String()}, &mcp.ServerOptions{
		Instructions: "SQL MCP server with defense-in-depth cost gate and RBAC. " +
			"Tools are gated by role permissions and a multi-layer cost gate; " +
			"unsafe writes and over-budget queries are rejected with rewrite hints.",
	})
	for _, t := range app.Tools.Enabled(app.ToolFlags) {
		registerTool(s, t, acquire)
	}
	for _, e := range app.Registry.Entities() {
		if e.Kind == entity.KindProcedure && e.MCP.CustomTool && e.MCP.TrustedProcedure {
			registerTool(s, tool.ProcedureTool{Entity: e}, acquire)
		}
	}
	release()
	registerSchemaResource(s, acquire)
	registerPrompts(s)
	return s
}

func registerTool(s *mcp.Server, t tool.Tool, acquire appAcquire) {
	info := t.Info()
	schema := info.InputSchema
	if len(schema) == 0 {
		// go-sdk requires an object-typed input schema. Tools that do not yet
		// declare a detailed schema get a permissive object; parameters are
		// still validated inside tool.Run.
		schema = json.RawMessage(`{"type":"object"}`)
	}
	mt := &mcp.Tool{
		Name:        info.Name,
		Description: info.Description,
		InputSchema: schema,
	}
	if info.ReadOnly {
		mt.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
	}
	handler := func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		app, release, err := acquire()
		if err != nil {
			return nil, err
		}
		defer release()
		role, subject := subjectFromContext(ctx, app.DefaultRole)
		tc := app.ToolContextForSubject(role, subject)
		if req.Session != nil {
			tc.Session = req.Session.ID()
		}
		currentRegistered, ok := currentTool(app, info.Name)
		if !ok {
			return toResult(tool.ErrUnauthorized)
		}
		res, err := tool.RunTool(ctx, currentRegistered, rawArgs(req), tc)
		if err != nil {
			return toResult(err)
		}
		return toMCPResult(res), nil
	}
	s.AddTool(mt, handler)
}

func currentTool(app *bootstrap.App, name string) (tool.Tool, bool) {
	if registered, ok := app.Tools.Get(name); ok {
		if registered.Enabled(app.ToolFlags) {
			return registered, true
		}
		return nil, false
	}
	for _, e := range app.Registry.Entities() {
		if e.Kind == entity.KindProcedure && e.MCP.CustomTool && e.MCP.TrustedProcedure &&
			tool.ProcedureToolName(e.Name) == name {
			return tool.ProcedureTool{Entity: e}, true
		}
	}
	return nil, false
}

func registerSchemaResource(s *mcp.Server, acquire appAcquire) {
	resource := &mcp.Resource{
		URI:         schemaResourceURI,
		Name:        "authorized-schema",
		Title:       "Authorized SQL schema",
		Description: "Entities and fields visible to the authenticated role and subject",
		MIMEType:    "application/json",
	}
	s.AddResource(resource, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if req.Params.URI != schemaResourceURI {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		app, release, err := acquire()
		if err != nil {
			return nil, err
		}
		defer release()
		role, subject := subjectFromContext(ctx, app.DefaultRole)
		payload, err := authorizedSchema(ctx, app, role, subject)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: schemaResourceURI, MIMEType: "application/json", Text: string(data),
		}}}, nil
	})
}

func authorizedSchema(
	ctx context.Context,
	app *bootstrap.App,
	role string,
	subject map[string]any,
) (map[string]any, error) {
	entities := make([]map[string]any, 0)
	for _, e := range app.Registry.Entities() {
		entry, ok, err := authorizedEntity(ctx, app, e, role, subject)
		if err != nil {
			return nil, err
		}
		if ok {
			entities = append(entities, entry)
		}
	}
	return map[string]any{"role": role, "entities": entities}, nil
}

func authorizedEntity(
	ctx context.Context,
	app *bootstrap.App,
	e entity.Entity,
	role string,
	subject map[string]any,
) (map[string]any, bool, error) {
	if e.Kind == entity.KindProcedure || !e.MCP.DMLTools {
		return nil, false, nil
	}
	read, err := app.Authorizer.Authorize(ctx, rbac.Request{
		Role: role, Subject: subject, Entity: e.Name, Action: entity.ActionRead,
	})
	if err != nil {
		return nil, false, err
	}
	aggregate, err := app.Authorizer.Authorize(ctx, rbac.Request{
		Role: role, Subject: subject, Entity: e.Name, Action: entity.ActionAggregate,
	})
	if err != nil {
		return nil, false, err
	}
	if !read.Allowed && !aggregate.Allowed {
		return nil, false, nil
	}
	fieldNames := read.Fields
	if !read.Allowed {
		fieldNames = aggregate.Fields
	}
	return map[string]any{
		"name": e.Name, "description": e.Description,
		"fields":    authorizedEntityFields(e, fieldNames),
		"actions":   authorizedEntityActions(read.Allowed, aggregate.Allowed),
		"rowScoped": e.RowPolicies[role] != nil,
	}, true, nil
}

func authorizedEntityFields(e entity.Entity, fieldNames []string) []map[string]any {
	fields := make([]map[string]any, 0, len(fieldNames))
	for _, name := range fieldNames {
		if attr, ok := e.AttributeByName(name); ok {
			fields = append(fields, map[string]any{
				"name": attr.Name, "alias": attr.Alias, "type": attr.Domain.Type,
				"description": attr.Description, "masked": attr.Mask != "",
			})
		}
	}
	return fields
}

func authorizedEntityActions(readAllowed, aggregateAllowed bool) []string {
	actions := make([]string, 0, 2)
	if readAllowed {
		actions = append(actions, "read")
	}
	if aggregateAllowed {
		actions = append(actions, "aggregate")
	}
	return actions
}

func registerPrompts(s *mcp.Server) {
	addPrompt := func(name, description, argument, text string) {
		s.AddPrompt(&mcp.Prompt{
			Name: name, Description: description,
			Arguments: []*mcp.PromptArgument{{
				Name: argument, Description: "User request or failed tool input", Required: true,
			}},
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Description: description,
				Messages: []*mcp.PromptMessage{{
					Role:    "user",
					Content: &mcp.TextContent{Text: text + "\n\nRequest:\n" + req.Params.Arguments[argument]},
				}},
			}, nil
		})
	}
	const (
		safeReadPrompt = "Use the authorized-schema resource first. Call only read_records, " +
			"select only visible fields, add the narrowest supported filter, and set a conservative limit. " +
			"Never invent entities or fields."
		safeAggregatePrompt = "Use the authorized-schema resource first. Call only aggregate_records " +
			"with visible fields, a narrow filter, and the minimum grouping needed. " +
			"Do not request raw rows or bypass row scope."
		rewriteQueryPrompt = "Rewrite the failed MCP tool input without weakening authorization or cost controls. " +
			"Preserve intent, narrow filters, reduce fields and limits, and follow returned cost-gate hints. " +
			"Never switch datasources or use raw SQL."
	)
	addPrompt("safe_read", "Build a bounded, authorized read_records call", "request", safeReadPrompt)
	addPrompt("safe_aggregate", "Build a bounded, authorized aggregate_records call", "request", safeAggregatePrompt)
	addPrompt("rewrite_query", "Rewrite a rejected request using safety hints", "request", rewriteQueryPrompt)
}

func rawArgs(req *mcp.CallToolRequest) json.RawMessage {
	return req.Params.Arguments
}

func toMCPResult(r tool.Result) *mcp.CallToolResult {
	out := &mcp.CallToolResult{IsError: r.IsError}
	b, _ := json.Marshal(r.Content)
	out.Content = []mcp.Content{&mcp.TextContent{Text: string(b)}}
	if r.StructuredResult != nil {
		out.StructuredContent = r.StructuredResult
	}
	return out
}

// toResult maps a core error to an MCP outcome. Business errors become
// IsError results (the agent can read and self-correct); overload/circuit and
// internal errors become protocol-level errors.
func toResult(err error) (*mcp.CallToolResult, error) {
	var ce *cost.ExceededError
	if errors.As(err, &ce) {
		text := err.Error()
		if len(ce.Hints) > 0 {
			text += "; hints: " + fmt.Sprint(ce.Hints)
		}
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil
	}
	switch {
	case errors.Is(err, tool.ErrUnauthorized),
		errors.Is(err, tool.ErrEntityNotFound),
		errors.Is(err, tool.ErrDMLToolsDisabled),
		errors.Is(err, tool.ErrUnsafeWrite),
		errors.Is(err, tool.ErrInvalidInput),
		errors.Is(err, tool.ErrTransactionNotFound),
		errors.Is(err, tool.ErrTransactionScope),
		errors.Is(err, tool.ErrTransactionCapacity),
		errors.Is(err, tool.ErrDatabase),
		errors.Is(err, tool.ErrNotImplemented):
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, nil
	}
	// Internal/provider errors are deliberately not reflected to clients because
	// driver messages may contain schema, SQL, constraint, or connection detail.
	return nil, errInternal
}

// ServeStdio runs the server on stdio.
func ServeStdio(ctx context.Context, s *mcp.Server) error {
	return s.Run(ctx, &mcp.StdioTransport{})
}

// subjectCtxKey keys the per-request caller identity on a context.
type subjectCtxKey struct{}

type requestSubject struct {
	role  string
	attrs map[string]any
}

// WithSubject attaches a per-request caller identity (role + attributes) to
// ctx. A trusted transport or gateway sets it after authenticating the caller;
// ServeHTTP derives it from request headers. Tool handlers read it to build a
// role- and tenant-scoped context, so a single process is no longer pinned to
// one role. Exported so custom transports can inject an authenticated subject.
func WithSubject(ctx context.Context, role string, attrs map[string]any) context.Context {
	return context.WithValue(ctx, subjectCtxKey{}, requestSubject{role: canonicalRole(role), attrs: attrs})
}

func subjectFromContext(ctx context.Context, defaultRole string) (string, map[string]any) {
	if s, ok := ctx.Value(subjectCtxKey{}).(requestSubject); ok {
		role := s.role
		if role == "" {
			role = canonicalRole(defaultRole)
		}
		return role, s.attrs
	}
	return canonicalRole(defaultRole), nil
}

func canonicalRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

// withRequestSubject extracts the caller's role and attributes from request
// headers onto the request context for tool handlers. It is only wired in when
// HTTPConfig.TrustProxyHeaders is set, i.e. behind a gateway that has already
// authenticated the caller; without it the headers are ignored and the default
// role applies, so a raw client cannot forge identity by setting a header.
//
//	X-MCP-Role:    the caller's role
//	X-MCP-Subject: optional JSON object of subject attributes (e.g. tenant_id)
func withRequestSubject(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get("X-MCP-Role")
		var attrs map[string]any
		if raw := r.Header.Get("X-MCP-Subject"); raw != "" {
			dec := json.NewDecoder(bytes.NewBufferString(raw))
			dec.UseNumber()
			if err := dec.Decode(&attrs); err != nil || attrs == nil {
				http.Error(w, "invalid X-MCP-Subject: expected a JSON object", http.StatusBadRequest)
				return
			}
			if err := dec.Decode(&struct{}{}); err != io.EOF {
				http.Error(w, "invalid X-MCP-Subject: expected a JSON object", http.StatusBadRequest)
				return
			}
		}
		if role != "" || attrs != nil {
			r = r.WithContext(WithSubject(r.Context(), role, attrs))
		}
		next.ServeHTTP(w, r)
	})
}

// HTTPConfig configures the streamable HTTP transport, including authentication
// and hardening. Zero-valued timeout/size fields receive safe defaults. Metrics
// is an optional handler mounted at /metrics.
type HTTPConfig struct {
	Addr              string
	Token             string // shared-secret bearer token; empty disables token auth
	TrustProxyHeaders bool   // trust X-MCP-Role/X-MCP-Subject identity headers
	TrustedProxyCIDRs []string
	TLSCert           string
	TLSKey            string
	ClientCA          string // PEM bundle; when set, require+verify a client cert (mTLS)
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
	Metrics           http.Handler
	SessionTimeout    time.Duration
	OnSessionClosed   func(string)
}

func (c HTTPConfig) tlsEnabled() bool  { return c.TLSCert != "" && c.TLSKey != "" }
func (c HTTPConfig) mtlsEnabled() bool { return c.ClientCA != "" }
func (c HTTPConfig) authConfigured() bool {
	return c.Token != "" || c.mtlsEnabled()
}

// isLoopbackAddr reports whether a listen address binds only the loopback
// interface. An empty host (e.g. ":8080") or a wildcard binds all interfaces
// and is treated as non-loopback (exposed).
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// validateHTTPSecurity enforces fail-closed startup: a listener exposed beyond
// loopback must configure authentication (token or mTLS); mTLS requires a
// server certificate.
func validateHTTPSecurity(c HTTPConfig) error {
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("mcpserver: tls.cert and tls.key must be configured together")
	}
	if !isLoopbackAddr(c.Addr) && !c.authConfigured() {
		return fmt.Errorf(
			"mcpserver: refusing to serve on non-loopback address %q without authentication: "+
				"set server.auth.token or server.auth.tls.clientCA, or bind to 127.0.0.1",
			c.Addr,
		)
	}
	if c.mtlsEnabled() && !c.tlsEnabled() {
		return errors.New("mcpserver: mTLS (clientCA) requires a server certificate (tls.cert/tls.key)")
	}
	if c.TrustProxyHeaders && !c.mtlsEnabled() && len(c.TrustedProxyCIDRs) == 0 {
		return errors.New("mcpserver: TrustProxyHeaders requires mTLS or at least one trusted proxy CIDR")
	}
	if _, err := parseTrustedProxyCIDRs(c.TrustedProxyCIDRs); err != nil {
		return err
	}
	return nil
}

func parseTrustedProxyCIDRs(values []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("mcpserver: invalid trusted proxy CIDR %q: %w", value, err)
		}
		nets = append(nets, network)
	}
	return nets, nil
}

func trustedProxyOnly(networks []*net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		for _, network := range networks {
			if ip != nil && network.Contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "untrusted proxy", http.StatusForbidden)
	})
}

// tokenAuth rejects requests lacking a matching bearer token. Comparison is
// constant-time to avoid leaking the token via timing.
func tokenAuth(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		scheme, presented, ok := strings.Cut(header, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			presented = ""
		}
		got := sha256.Sum256([]byte(strings.TrimSpace(presented)))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody caps the request body size to guard against unbounded reads.
func limitBody(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

type sessionEventStore struct {
	mcp.EventStore
	onClosed func(string)
	identity *sessionIdentityStore
}

func (s *sessionEventStore) SessionClosed(ctx context.Context, sessionID string) error {
	err := s.EventStore.SessionClosed(ctx, sessionID)
	if s.identity != nil {
		s.identity.close(sessionID)
	}
	if s.onClosed != nil {
		s.onClosed(sessionID)
	}
	return err
}

const sessionIDHeader = "Mcp-Session-Id"

type sessionIdentity struct {
	role    string
	subject string
}

type sessionIdentityStore struct {
	mu       sync.RWMutex
	sessions map[string]sessionIdentity
}

func newSessionIdentityStore() *sessionIdentityStore {
	return &sessionIdentityStore{sessions: make(map[string]sessionIdentity)}
}

func requestIdentity(ctx context.Context) sessionIdentity {
	subject, _ := ctx.Value(subjectCtxKey{}).(requestSubject)
	role := strings.TrimSpace(subject.role)
	attrs := ""
	if len(subject.attrs) > 0 {
		// encoding/json sorts map keys, giving semantically identical header
		// objects one stable identity regardless of input key order.
		if encoded, err := json.Marshal(subject.attrs); err == nil {
			attrs = string(encoded)
		}
	}
	return sessionIdentity{role: role, subject: attrs}
}

func (s *sessionIdentityStore) bind(sessionID string, identity sessionIdentity) bool {
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sessions[sessionID]; ok {
		return existing == identity
	}
	s.sessions[sessionID] = identity
	return true
}

func (s *sessionIdentityStore) matches(sessionID string, identity sessionIdentity) bool {
	s.mu.RLock()
	existing, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	return ok && existing == identity
}

func (s *sessionIdentityStore) close(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
}

func bindSessionIdentity(store *sessionIdentityStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := requestIdentity(r.Context())
		sessionID := strings.TrimSpace(r.Header.Get(sessionIDHeader))
		switch r.Method {
		case http.MethodPost, http.MethodGet, http.MethodDelete:
			if sessionID != "" && !store.matches(sessionID, identity) {
				http.Error(w, "MCP session identity mismatch", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
		if sessionID == "" && r.Method == http.MethodPost {
			_ = store.bind(strings.TrimSpace(w.Header().Get(sessionIDHeader)), identity)
		}
	})
}
