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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/version"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

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
		Instructions: serverInstructions,
	})
	tools := &listedTools{acquire: acquire, defs: map[string]string{}}
	tools.sync(s, app)
	release()
	s.AddReceivingMiddleware(listEscapeHatchPerUser(acquire))
	registerSchemaResource(s, acquire)
	registerPrompts(s)
	return s, tools
}

const serverInstructions = "Governed SQL access: you work with configured entities (tables, views " +
	"and procedures), never with raw SQL, and only with the entities, fields and rows your role is granted. " +
	"Start with describe_entities: search it by keyword to find entities, then describe one to learn its " +
	"fields and keys; use only names it returns. Read with read_records or aggregate_records, selecting " +
	"few fields, filtering narrowly and setting a limit. A refused call returns isError with a structured " +
	"denial {code, reason, retryable, constraints, hints}: when retryable, follow the hints (narrow the " +
	"filter, lower the limit, pick fields) and retry; RATE_LIMITED, OVERLOADED and DATASOURCE_UNAVAILABLE " +
	"mean waiting before retrying; UNAUTHORIZED is final, so do not probe for other entities or fields."

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
	output := rowsOutputSchema
	if info.Object {
		output = objectOutputSchema
	}
	closedWorld := false
	return &mcp.Tool{
		Name:         info.Name,
		Title:        info.Title,
		Description:  info.Description,
		InputSchema:  schema,
		OutputSchema: output,
		// Every tool works on the configured databases alone, a closed world.
		Annotations: &mcp.ToolAnnotations{
			Title: info.Title, ReadOnlyHint: info.ReadOnly, IdempotentHint: info.Idempotent,
			DestructiveHint: destructiveHint(info), OpenWorldHint: &closedWorld,
		},
	}
}

// destructiveHint is unset for a read-only tool, where it does not apply.
func destructiveHint(info tool.Info) *bool {
	if info.ReadOnly {
		return nil
	}
	return &info.Destructive
}

// A call's structured result, which its text repeats as JSON: rows are
// {"result": [...]} (with nextCursor when an ordered read has a next page,
// truncated when a write's rows were cut short), and a tool returning one
// object returns it as is.
var (
	rowsOutputSchema = json.RawMessage(`{"type":"object","properties":{"result":` +
		`{"type":"array","items":{"type":"object"}},"nextCursor":{"type":"object"},` +
		`"truncated":{"type":"boolean"}},"required":["result"]}`)
	objectOutputSchema = json.RawMessage(`{"type":"object"}`)
)

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
		return toMCPResult(res, currentRegistered.Info()), nil
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

func rawArgs(req *mcp.CallToolRequest) json.RawMessage {
	return req.Params.Arguments
}

func toMCPResult(r tool.Result, info tool.Info) *mcp.CallToolResult {
	rows := map[string]any{"result": r.Content}
	if r.NextCursor != nil {
		rows["nextCursor"] = r.NextCursor
	}
	if r.Truncated {
		rows["truncated"] = true
	}
	var structured any = rows
	if info.Object && len(r.Content) == 1 {
		structured = r.Content[0]
	}
	b, _ := json.Marshal(structured)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		StructuredContent: json.RawMessage(b),
	}
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
// role is a role, never a user's principal: a configured user is
// authenticated by its token or X-MCP-User, and a role naming a principal
// ("user:alice") holds no grant.
func WithSubject(ctx context.Context, role string, attrs map[string]any) context.Context {
	return context.WithValue(ctx, subjectCtxKey{}, requestSubject{role: rbac.NormalizeRole(role), attrs: attrs})
}

func subjectFromContext(ctx context.Context, defaultRole string) (string, map[string]any) {
	if s, ok := ctx.Value(subjectCtxKey{}).(requestSubject); ok {
		role := s.role
		if role == "" {
			role = rbac.NormalizeRole(defaultRole)
		}
		return role, s.attrs
	}
	return rbac.NormalizeRole(defaultRole), nil
}

// withRequestSubject extracts the caller's role and attributes from request
// headers onto the request context for tool handlers. It is only wired in when
// HTTPConfig.TrustProxyHeaders is set, i.e. behind a gateway that has already
// authenticated the caller; without it the headers are ignored and the default
// role applies, so a raw client cannot forge identity by setting a header.
//
//	X-MCP-Role:    the caller's role; a principal ("user:alice") is refused,
//	               users are named by X-MCP-User
//	X-MCP-Subject: optional JSON object of subject attributes (e.g. tenant_id)
func withRequestSubject(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get("X-MCP-Role")
		if namesPrincipal(role) {
			http.Error(w, "X-MCP-Role names a role, not a principal; name a user with X-MCP-User",
				http.StatusBadRequest)
			return
		}
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
	// HTTPAuth is how callers are authenticated; a reload may replace it
	// (see AuthChanges).
	HTTPAuth
	// Bus runs the server's background tasks; optional.
	Bus               *bus.Bus
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
	// BodyReadTimeout bounds reading a request body (default 30s): a client
	// sending it slowly cannot hold a connection. It is lifted once the body
	// is read, so streamed responses (SSE) are not cut.
	BodyReadTimeout time.Duration
	Metrics         http.Handler
	SessionTimeout  time.Duration
	OnSessionClosed func(string)
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
	Token             string // shared-secret bearer token; empty disables token auth
	TrustProxyHeaders bool   // trust X-MCP-User/X-MCP-Role/X-MCP-Subject identity headers from proxies
	TrustedProxyCIDRs []string
	// TrustedProxyCertSubjects names, under mTLS, the proxies' client
	// certificates (leaf CN or DNS SAN); other certificates are callers.
	TrustedProxyCertSubjects []string
	TLSCert                  string
	TLSKey                   string
	ClientCA                 string // PEM bundle; when set, require+verify a client cert (mTLS)
	// Users resolves configured users; nil means no users are configured.
	// With users, bearer tokens may identify individual users and trusted
	// proxies may send X-MCP-User.
	Users UserDirectory
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
	c := HTTPConfig{Addr: addr, HTTPAuth: auth}
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

// validateHTTPSecurity enforces fail-closed startup: a listener exposed beyond
// loopback must configure authentication (token or mTLS); mTLS requires a
// server certificate.
func validateHTTPSecurity(c HTTPConfig) error {
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("mcpserver: tls.cert and tls.key must be configured together")
	}
	if err := config.CheckSharedToken(c.Addr, c.Token); err != nil {
		return fmt.Errorf("mcpserver: %w", err)
	}
	if !config.IsLoopbackAddr(c.Addr) && !c.authConfigured() {
		return fmt.Errorf(
			"mcpserver: refusing to serve on non-loopback address %q without authentication: "+
				"configure users, server.auth.token or server.auth.tls.clientCA, or bind to 127.0.0.1",
			c.Addr,
		)
	}
	if c.mtlsEnabled() && !c.tlsEnabled() {
		return errors.New("mcpserver: mTLS (clientCA) requires a server certificate (tls.cert/tls.key)")
	}
	if len(c.TrustedProxyCertSubjects) > 0 && !c.mtlsEnabled() {
		return errors.New("mcpserver: trusted proxy certificate subjects require mTLS (clientCA)")
	}
	if c.TrustProxyHeaders && len(c.TrustedProxyCIDRs) == 0 && len(c.TrustedProxyCertSubjects) == 0 {
		return errors.New("mcpserver: TrustProxyHeaders requires a trusted proxy CIDR or, under mTLS, " +
			"a trusted proxy certificate subject")
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

// trustedProxy serves a request from a trusted proxy (a source address in
// networks, or a client certificate named in subjects) with proxied, which
// reads its identity headers. Any other request is refused unless mTLS
// authenticated its channel: then it is served with direct when it carries no
// identity header, since holding a certificate does not make a caller a proxy.
func trustedProxy(networks []*net.IPNet, subjects []string, mtls bool, proxied, direct http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case fromProxyAddress(networks, r) || fromProxyCertificate(subjects, r):
			proxied.ServeHTTP(w, r)
		case mtls && !hasIdentityHeader(r):
			direct.ServeHTTP(w, r)
		default:
			http.Error(w, "untrusted proxy", http.StatusForbidden)
		}
	})
}

func fromProxyAddress(networks []*net.IPNet, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && slices.ContainsFunc(networks, func(n *net.IPNet) bool { return n.Contains(ip) })
}

// fromProxyCertificate reports whether the leaf certificate of r's
// connection (verified by requireClientCert) has a CN or DNS SAN in subjects.
func fromProxyCertificate(subjects []string, r *http.Request) bool {
	if len(subjects) == 0 || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	leaf := r.TLS.PeerCertificates[0]
	return slices.Contains(subjects, leaf.Subject.CommonName) ||
		slices.ContainsFunc(leaf.DNSNames, func(name string) bool { return slices.Contains(subjects, name) })
}

func hasIdentityHeader(r *http.Request) bool {
	return r.Header.Get(userHeader) != "" || r.Header.Get("X-MCP-Role") != "" || r.Header.Get("X-MCP-Subject") != ""
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
func limitBody(maxBytes int64, readTimeout time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody && readTimeout > 0 {
			rc := http.NewResponseController(w)
			if rc.SetReadDeadline(time.Now().Add(readTimeout)) == nil {
				body := &deadlineBody{ReadCloser: r.Body, lift: func() { _ = rc.SetReadDeadline(time.Time{}) }}
				defer body.done()
				r.Body = body
			}
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// deadlineBody lifts the read deadline set for reading it once it is read
// or closed.
type deadlineBody struct {
	io.ReadCloser
	lift   func()
	lifted sync.Once
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.done()
	}
	return n, err
}

func (b *deadlineBody) Close() error {
	b.done()
	return b.ReadCloser.Close()
}

func (b *deadlineBody) done() { b.lifted.Do(b.lift) }

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
