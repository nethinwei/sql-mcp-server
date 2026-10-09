package mcpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
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
// snapshot for every request. The tools it lists follow every snapshot a
// reload publishes; clients are told the list changed.
func NewRuntimeServer(runtime *bootstrap.Runtime) *mcp.Server {
	s, tools := newServerWithTools(runtime.Acquire)
	runtime.OnPublish(func(app *bootstrap.App) { tools.sync(s, app) })
	return s
}

func newServer(acquire appAcquire) *mcp.Server {
	s, _ := newServerWithTools(acquire)
	return s
}

func newServerWithTools(acquire appAcquire) (*mcp.Server, *listedTools) {
	app, release, err := acquire()
	if err != nil {
		panic(err)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "sql-mcp-server", Version: version.String()}, &mcp.ServerOptions{
		Instructions: "SQL MCP server with defense-in-depth cost gate and RBAC. " +
			"Tools are gated by role permissions and a multi-layer cost gate; " +
			"unsafe writes and over-budget queries are rejected with rewrite hints.",
	})
	tools := &listedTools{acquire: acquire, defs: map[string]string{}}
	tools.sync(s, app)
	release()
	registerSchemaResource(s, acquire)
	registerPrompts(s)
	return s, tools
}

// listedTools are the tools a server lists, by name with their definition,
// so that a new configuration's tools replace them.
type listedTools struct {
	mu      sync.Mutex
	acquire appAcquire
	defs    map[string]string
}

// sync lists app's enabled and procedure tools: it removes the others and
// (re)registers the new and the changed ones, which notifies clients.
func (l *listedTools) sync(s *mcp.Server, app *bootstrap.App) {
	want := map[string]tool.Tool{}
	for _, t := range app.Tools.Enabled(app.ToolFlags) {
		want[t.Info().Name] = t
	}
	for _, t := range tool.ProcedureTools(app.Registry) {
		want[t.Info().Name] = t
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var gone []string
	for name := range l.defs {
		if _, ok := want[name]; !ok {
			gone = append(gone, name)
			delete(l.defs, name)
		}
	}
	if len(gone) > 0 {
		s.RemoveTools(gone...)
	}
	for name, t := range want {
		mt := mcpTool(t)
		def, _ := json.Marshal(mt)
		if l.defs[name] != string(def) {
			registerTool(s, mt, t, l.acquire)
			l.defs[name] = string(def)
		}
	}
}

// mcpTool is how a tool is listed.
func mcpTool(t tool.Tool) *mcp.Tool {
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
	return mt
}

func registerTool(s *mcp.Server, mt *mcp.Tool, t tool.Tool, acquire appAcquire) {
	info := t.Info()
	handler := func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		app, release, err := acquire()
		if err != nil {
			return nil, err
		}
		defer release()
		role, subject := callerIdentity(ctx, app)
		tc := app.ToolContextForSubject(role, subject)
		if req.Session != nil {
			tc.Session = req.Session.ID()
		}
		tc.DecisionID = tool.NewDecisionID()
		currentRegistered, ok := currentTool(app, info.Name)
		if !ok {
			return toResult(tool.ErrUnauthorized, tc.DecisionID)
		}
		res, err := tool.RunTool(ctx, currentRegistered, rawArgs(req), tc)
		if err != nil {
			return toResult(err, tc.DecisionID)
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
	if t, ok := tool.FindProcedureTool(app.Registry, name); ok {
		return t, true
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
		role, subject := callerIdentity(ctx, app)
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
	// Read and aggregate grants may cover different fields, so each action
	// reports its own fields and scopes; top-level fields describe them all.
	actions := make([]string, 0, 2)
	access := map[string]any{}
	union := map[string]bool{}
	for _, action := range []entity.Action{entity.ActionRead, entity.ActionAggregate} {
		dec, err := app.Authorizer.Authorize(ctx, rbac.Request{
			Role: role, Subject: subject, Entity: e.Name, Action: action,
		})
		if err != nil {
			return nil, false, err
		}
		a := accessOf(e, dec)
		if !a.allowed {
			continue
		}
		actions = append(actions, action.String())
		access[action.String()] = a.describe()
		for _, f := range a.fields {
			union[f] = true
		}
	}
	if len(actions) == 0 {
		return nil, false, nil
	}
	return map[string]any{
		"name": e.Name, "description": e.Description, "datasource": e.DatasourceName(),
		"fields":    authorizedEntityFields(e, e.OrderedNames(union)),
		"keys":      tool.VisibleKeys(e, e.OrderedNames(union)),
		"actions":   actions,
		"access":    access,
		"rowScoped": e.RowPolicies[role] != nil,
	}, true, nil
}

// entityAccess is what a role may do with an entity's fields for one action.
// scopes is set when the fields are split across grants with incompatible
// field scopes, so the default projection is denied but explicit field
// selections within one scope are allowed.
type entityAccess struct {
	allowed bool
	fields  []string
	scopes  [][]string
}

func accessOf(e entity.Entity, dec rbac.Decision) entityAccess {
	if dec.Allowed {
		return entityAccess{allowed: true, fields: dec.Fields}
	}
	if !dec.Reachable() {
		return entityAccess{}
	}
	union := map[string]bool{}
	scopes := dec.Scopes()
	for _, scope := range scopes {
		for _, f := range scope {
			union[f] = true
		}
	}
	return entityAccess{allowed: true, fields: e.OrderedNames(union), scopes: scopes}
}

// describe is the resource entry for one action. When the fields are split
// across grants, a call must select fields explicitly, all within one scope.
func (a entityAccess) describe() map[string]any {
	out := map[string]any{"fields": a.fields}
	if a.scopes != nil {
		out["explicitFieldsRequired"] = true
		out["fieldScopes"] = a.scopes
	}
	return out
}

func authorizedEntityFields(e entity.Entity, fieldNames []string) []map[string]any {
	fields := make([]map[string]any, 0, len(fieldNames))
	for _, name := range fieldNames {
		if attr, ok := e.AttributeByName(name); ok {
			fields = append(fields, map[string]any{
				"name": attr.Name, "alias": attr.Alias, "type": attr.Domain.Type,
				"description": attr.Description, "masked": attr.Mask != "",
				"required": attr.Domain.Required(), "readOnly": !attr.Domain.Writable(),
			})
		}
	}
	return fields
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
			"select only fields listed in access.read (all within one access.read.fieldScopes entry when " +
			"explicitFieldsRequired is set), " +
			"add the narrowest supported filter, and set a conservative limit. " +
			"Never invent entities or fields."
		safeAggregatePrompt = "Use the authorized-schema resource first. Call only aggregate_records " +
			"with fields listed in access.aggregate (within one of its fieldScopes when explicitFieldsRequired " +
			"is set), a narrow filter, and the minimum grouping needed. " +
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
	b, _ := json.Marshal(r.Content)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// toResult maps a core error to an MCP outcome. Business errors become
// IsError results carrying the machine-readable tool.Denial contract in
// StructuredContent (the agent can parse and self-correct); overload/circuit
// and internal errors become protocol-level errors.
func toResult(err error, decisionID string) (*mcp.CallToolResult, error) {
	denial, ok := tool.DenialFor(err, decisionID)
	if !ok {
		// Internal/provider errors are deliberately not reflected to clients
		// because driver messages may contain schema, SQL, constraint, or
		// connection detail.
		return nil, errInternal
	}
	text := denial.Reason
	if len(denial.Hints) > 0 {
		text += "; hints: " + strings.Join(denial.Hints, "; ")
	}
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: denial,
	}, nil
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
	user  bool // role is a configured user's principal
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
		attrs, err := parseSubjectHeader(r.Header.Get("X-MCP-Subject"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if role != "" || attrs != nil {
			r = r.WithContext(WithSubject(r.Context(), role, attrs))
		}
		next.ServeHTTP(w, r)
	})
}

// parseSubjectHeader decodes X-MCP-Subject; empty yields nil attributes.
func parseSubjectHeader(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var attrs map[string]any
	errInvalid := errors.New("invalid X-MCP-Subject: expected a JSON object")
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.UseNumber()
	if err := dec.Decode(&attrs); err != nil || attrs == nil {
		return nil, errInvalid
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, errInvalid
	}
	return attrs, nil
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
	// Users resolves configured users; nil means no users are configured.
	// With users, bearer tokens may identify individual users and trusted
	// proxies may send X-MCP-User.
	Users UserDirectory
	// RevokedPrincipals registers a callback receiving users removed or
	// disabled by a reload; their sessions are dropped and rolled back, and
	// the callback returns those sessions.
	RevokedPrincipals func(func([]string) []string)
	// SnapshotReady backs /readyz/snapshot: it returns nil when a
	// configuration snapshot is published and servable. DatabaseReady backs
	// /readyz/db: it returns nil when the configured databases are reachable.
	// Probes fail closed: a nil probe or a probe error yields 503.
	SnapshotReady func(context.Context) error
	DatabaseReady func(context.Context) error
	// Admin, when set, serves /admin/ with its own authentication; MCP bearer
	// tokens do not apply there and admin sessions do not apply to /mcp.
	Admin http.Handler
	// SnapshotStale returns the ID of a published store revision that is not
	// applied (0 when none); it adds X-Snapshot-Stale to /readyz/snapshot.
	SnapshotStale func() int64
	// AuthChanges, when set, receives a function switching to a new
	// authentication while serving, prepared with PrepareHTTPAuth: switching
	// reads no file, so it cannot fail half way. It fails, keeping the
	// current one, only when it would switch TLS on or off.
	AuthChanges func(apply func(PreparedAuth) error)
}

// HTTPAuth is how HTTP callers authenticate: the part of HTTPConfig a
// reload may change while serving (see HTTPConfig.AuthChanges). The address,
// and serving TLS or not, are fixed for the listener.
type HTTPAuth struct {
	Token             string
	TrustProxyHeaders bool
	TrustedProxyCIDRs []string
	TLSCert           string
	TLSKey            string
	ClientCA          string
	Users             UserDirectory
}

func (c HTTPConfig) auth() HTTPAuth {
	return HTTPAuth{
		Token: c.Token, TrustProxyHeaders: c.TrustProxyHeaders, TrustedProxyCIDRs: c.TrustedProxyCIDRs,
		TLSCert: c.TLSCert, TLSKey: c.TLSKey, ClientCA: c.ClientCA, Users: c.Users,
	}
}

func (c *HTTPConfig) setAuth(a HTTPAuth) {
	c.Token, c.TrustProxyHeaders, c.TrustedProxyCIDRs = a.Token, a.TrustProxyHeaders, a.TrustedProxyCIDRs
	c.TLSCert, c.TLSKey, c.ClientCA, c.Users = a.TLSCert, a.TLSKey, a.ClientCA, a.Users
}

// PreparedAuth is an authentication PrepareHTTPAuth checked and whose
// certificates it read, ready to switch to.
type PreparedAuth struct {
	auth HTTPAuth
	tls  *tls.Config
}

// PrepareHTTPAuth checks that a listener on addr may authenticate callers as
// auth does, under the rules ServeHTTP starts with, and reads its
// certificates: a reload prepares the authentication it would switch to, and
// fails when it cannot.
func PrepareHTTPAuth(addr string, auth HTTPAuth) (PreparedAuth, error) {
	c := HTTPConfig{Addr: addr}
	c.setAuth(auth)
	if err := validateHTTPSecurity(c); err != nil {
		return PreparedAuth{}, err
	}
	prepared := PreparedAuth{auth: auth}
	if c.tlsEnabled() {
		var err error
		if prepared.tls, err = loadTLS(c); err != nil {
			return PreparedAuth{}, err
		}
	}
	return prepared, nil
}

// WithUsers returns p resolving its users with users, when it has users.
func (p PreparedAuth) WithUsers(users UserDirectory) PreparedAuth {
	if p.auth.Users != nil {
		p.auth.Users = users
	}
	return p
}

func (c HTTPConfig) tlsEnabled() bool  { return c.TLSCert != "" && c.TLSKey != "" }
func (c HTTPConfig) mtlsEnabled() bool { return c.ClientCA != "" }
func (c HTTPConfig) authConfigured() bool {
	return c.Token != "" || c.mtlsEnabled() || c.Users != nil
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
				"configure users, server.auth.token or server.auth.tls.clientCA, or bind to 127.0.0.1",
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
