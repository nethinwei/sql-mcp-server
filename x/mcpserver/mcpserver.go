package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
// headers onto the request context for tool handlers. Headers MUST be set by a
// trusted gateway that has already authenticated the caller (full OAuth
// verification is on the roadmap); never expose this port directly to
// untrusted clients.
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

// ServeHTTP runs the server on streamable HTTP at addr, with a /healthz check.
func ServeHTTP(ctx context.Context, s *mcp.Server, addr string) error {
	handler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return s }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", withRequestSubject(handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
