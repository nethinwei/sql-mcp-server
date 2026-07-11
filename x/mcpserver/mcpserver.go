package mcpserver

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// NewServer builds an mcp.Server with the app's enabled tools registered.
func NewServer(app *bootstrap.App) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "sql-mcp-server", Version: "v0.1.0"}, &mcp.ServerOptions{
		Instructions: "SQL MCP server with defense-in-depth cost gate and RBAC. " +
			"Tools are gated by role permissions and a multi-layer cost gate; " +
			"unsafe writes and over-budget queries are rejected with rewrite hints.",
	})
	for _, t := range app.Tools.Enabled(app.ToolFlags) {
		registerTool(s, t, app)
	}
	return s
}

func registerTool(s *mcp.Server, t tool.Tool, app *bootstrap.App) {
	info := t.Info()
	schema := info.InputSchema
	if len(schema) == 0 {
		// go-sdk requires an object-typed input schema. Tools that do not yet
		// declare a detailed schema get a permissive object; parameters are
		// still validated inside tool.Run. Detailed schemas are P1.
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
		role, subject := subjectFromContext(ctx, app.DefaultRole)
		tc := app.ToolContextForSubject(role, subject)
		res, err := tool.RunTool(ctx, t, rawArgs(req), tc)
		if err != nil {
			return toResult(err)
		}
		return toMCPResult(res), nil
	}
	s.AddTool(mt, handler)
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
		errors.Is(err, tool.ErrUnsafeWrite),
		errors.Is(err, tool.ErrInvalidInput),
		errors.Is(err, tool.ErrNotImplemented):
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, nil
	}
	// ErrOverloaded / ErrCircuitOpen / internal -> protocol-level error.
	return nil, err
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
	return context.WithValue(ctx, subjectCtxKey{}, requestSubject{role: role, attrs: attrs})
}

func subjectFromContext(ctx context.Context, defaultRole string) (string, map[string]any) {
	if s, ok := ctx.Value(subjectCtxKey{}).(requestSubject); ok {
		role := s.role
		if role == "" {
			role = defaultRole
		}
		return role, s.attrs
	}
	return defaultRole, nil
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
			_ = json.Unmarshal([]byte(raw), &attrs)
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
	if !isLoopbackAddr(c.Addr) && !c.authConfigured() {
		return fmt.Errorf("mcpserver: refusing to serve on non-loopback address %q without authentication: set server.auth.token or server.auth.tls.clientCA, or bind to 127.0.0.1", c.Addr)
	}
	if c.mtlsEnabled() && !c.tlsEnabled() {
		return errors.New("mcpserver: mTLS (clientCA) requires a server certificate (tls.cert/tls.key)")
	}
	return nil
}

// tokenAuth rejects requests lacking a matching bearer token. Comparison is
// constant-time to avoid leaking the token via timing.
func tokenAuth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
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

// ServeHTTP runs the server on streamable HTTP with authentication, request
// hardening (timeouts, header/body caps), a /healthz check, and an optional
// /metrics endpoint. See HTTPConfig for the security model.
func ServeHTTP(ctx context.Context, s *mcp.Server, cfg HTTPConfig) error {
	if err := validateHTTPSecurity(cfg); err != nil {
		return err
	}
	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = 10 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 120 * time.Second
	}
	if cfg.MaxHeaderBytes <= 0 {
		cfg.MaxHeaderBytes = 1 << 20 // 1 MiB
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 4 << 20 // 4 MiB
	}

	var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return s }, nil)
	// Identity headers are trusted only behind an authenticating gateway.
	if cfg.TrustProxyHeaders {
		mcpHandler = withRequestSubject(mcpHandler)
	}
	mcpHandler = limitBody(cfg.MaxBodyBytes, mcpHandler)
	if cfg.Token != "" {
		mcpHandler = tokenAuth(cfg.Token, mcpHandler)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	if cfg.Metrics != nil {
		// Metrics carry only aggregate counters, but when a token is configured
		// (typically an exposed listener) protect /metrics too rather than
		// leaving it open. /healthz stays open for liveness probes.
		metricsHandler := cfg.Metrics
		if cfg.Token != "" {
			metricsHandler = tokenAuth(cfg.Token, metricsHandler)
		}
		mux.Handle("/metrics", metricsHandler)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
	if cfg.mtlsEnabled() {
		pool := x509.NewCertPool()
		pem, err := os.ReadFile(cfg.ClientCA)
		if err != nil {
			return fmt.Errorf("mcpserver: read client CA: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("mcpserver: client CA file contains no valid certificates")
		}
		srv.TLSConfig = &tls.Config{
			ClientCAs:  pool,
			ClientAuth: tls.RequireAndVerifyClientCert,
			MinVersion: tls.VersionTLS12,
		}
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	var err error
	if cfg.tlsEnabled() {
		err = srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
