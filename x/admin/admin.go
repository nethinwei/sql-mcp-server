// Package admin serves the admin API under /admin: sign-in with local
// accounts, cookie sessions with CSRF tokens, and the GraphQL endpoint.
package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/graph"
	"github.com/nethinwei/sql-mcp-server/x/admin/ui"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// Query limits.
const (
	maxQueryDepth      = 10
	maxQueryComplexity = 1000
	maxLoginBody       = 4 << 10
)

const (
	cookieName = "smcp_admin"
	csrfHeader = "X-CSRF-Token"
)

// Config wires the admin API.
type Config struct {
	Store      revision.Store
	Accounts   accounts.Store
	Introspect graph.Introspection
	// Status reports the serving process to the console; optional.
	Status func() graph.RuntimeState
	// Capabilities reports what the serving connections may do; optional.
	Capabilities func() bootstrap.EntityCapabilities
	// SecureCookie marks the session cookie Secure; set it when serving TLS.
	SecureCookie bool
	// Playground mounts GraphiQL at /admin/playground and enables GraphQL
	// schema introspection. It is a development aid.
	Playground bool
	Now        func() time.Time
}

// Handler serves /admin/*.
type Handler struct {
	cfg      Config
	sessions *sessions
	logins   *loginGate
	graphql  http.Handler
	mux      *http.ServeMux
}

// New builds the admin handler.
func New(cfg Config) *Handler {
	h := &Handler{cfg: cfg, sessions: newSessions(cfg.Now), logins: newLoginGate(maxConcurrentLogins)}
	srv := handler.New(graph.NewExecutableSchema(graph.Config{Resolvers: &graph.Resolver{
		Store: cfg.Store, Accounts: accounts.Service{Store: cfg.Accounts}, Introspect: cfg.Introspect, Status: cfg.Status,
		Capabilities: cfg.Capabilities,
	}}))
	srv.AddTransport(transport.POST{})
	srv.Use(extension.FixedComplexityLimit(maxQueryComplexity))
	srv.Use(depthLimit(maxQueryDepth))
	if cfg.Playground {
		srv.Use(extension.Introspection{})
	}
	h.graphql = srv
	h.mux = http.NewServeMux()
	h.mux.HandleFunc("POST /admin/login", h.login)
	h.mux.HandleFunc("GET /admin/session", h.currentSession)
	h.mux.Handle("GET /admin/", ui.Handler("/admin/"))
	h.mux.HandleFunc("POST /admin/logout", h.logout)
	h.mux.Handle("POST /admin/graphql", h.authenticated(true, h.graphql))
	h.mux.HandleFunc("GET /admin/graphql", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GraphQL accepts POST only"})
	})
	if cfg.Playground {
		h.mux.HandleFunc("GET /admin/playground", h.playground)
	}
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// login verifies a local account and starts a session. Failures do not tell
// unknown accounts from wrong passwords and lock a name after repeated
// failures.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxLoginBody)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected JSON {username, password}"})
		return
	}
	username := strings.ToLower(strings.TrimSpace(in.Username))
	if !h.sessions.allowLogin(username) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": errLockedOut.Error()})
		return
	}
	if !h.logins.enter(r.Context()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": errBusy.Error()})
		return
	}
	defer h.logins.leave()
	account, err := h.cfg.Accounts.GetAdmin(r.Context(), username)
	if err != nil && !errors.Is(err, configstore.ErrAdminNotFound) {
		slog.Error("admin sign-in lookup failed", "error", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "sign-in failed"})
		return
	}
	ok := auth.VerifyPassword(account.PasswordHash, in.Password) && err == nil && !account.Disabled
	h.sessions.recordLogin(username, ok)
	if !ok {
		slog.Warn("admin sign-in rejected", "username", username)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
		return
	}
	id, csrf, err := h.sessions.create(account.Username, credentialTag(account.PasswordHash))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: id, Path: "/admin", HttpOnly: true, Secure: h.cfg.SecureCookie,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionAbsoluteTimeout.Seconds()),
	})
	slog.Info("admin signed in", "username", account.Username)
	writeJSON(w, http.StatusOK, map[string]any{
		"username": account.Username, "permissions": account.Permissions, "csrfToken": csrf,
	})
}

// currentSession restores the console after a reload: it returns the signed-in
// account and the session's CSRF token, which cross-site pages cannot read.
func (h *Handler) currentSession(w http.ResponseWriter, r *http.Request) {
	sess, principal, ok := h.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": auth.ErrUnauthorized.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": principal.Username, "permissions": principal.Permissions, "csrfToken": sess.csrf,
	})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		if sess, ok := h.sessions.lookup(c.Value); ok && r.Header.Get(csrfHeader) == sess.csrf {
			h.sessions.delete(c.Value)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Path: "/admin", HttpOnly: true, Secure: h.cfg.SecureCookie,
		SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// session resolves the request's session and re-reads its account, so a
// disabled or deleted account, or one whose password changed, loses access on
// its next request.

// credentialTag fingerprints a password hash for comparison with a session.
func credentialTag(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) session(r *http.Request) (session, auth.Principal, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, auth.Principal{}, false
	}
	sess, ok := h.sessions.lookup(c.Value)
	if !ok {
		return session{}, auth.Principal{}, false
	}
	account, err := h.cfg.Accounts.GetAdmin(r.Context(), sess.username)
	if err != nil || account.Disabled || credentialTag(account.PasswordHash) != sess.credential {
		h.sessions.delete(c.Value)
		return session{}, auth.Principal{}, false
	}
	return sess, auth.Principal{Username: account.Username, Permissions: account.Permissions}, true
}

// authenticated requires a session and, when checkCSRF is set, the session's
// CSRF token in X-CSRF-Token.
func (h *Handler) authenticated(checkCSRF bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, principal, ok := h.session(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": auth.ErrUnauthorized.Error()})
			return
		}
		if checkCSRF && r.Header.Get(csrfHeader) != sess.csrf {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing or invalid " + csrfHeader})
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
}

func (h *Handler) playground(w http.ResponseWriter, r *http.Request) {
	sess, _, ok := h.session(r)
	if !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, loginPage)
		return
	}
	playground.Handler("sql-mcp-server admin", "/admin/graphql",
		playground.WithGraphiqlFetcherHeaders(map[string]string{csrfHeader: sess.csrf}),
		playground.WithGraphiqlEnablePluginExplorer(true),
	).ServeHTTP(w, r)
}

const loginPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>sql-mcp-server admin</title>
<style>body{font:15px system-ui;max-width:22rem;margin:5rem auto}input,button{display:block;width:100%;
margin:.4rem 0;padding:.5rem;font:inherit}#err{color:#b00}</style></head>
<body><h2>sql-mcp-server admin</h2>
<form id="f"><input name="username" placeholder="username" autocomplete="username" required>
<input name="password" type="password" placeholder="password" autocomplete="current-password" required>
<button>Sign in</button><div id="err"></div></form>
<script>
document.getElementById('f').onsubmit = async (e) => {
  e.preventDefault();
  const f = new FormData(e.target);
  const res = await fetch('/admin/login', {method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({username: f.get('username'), password: f.get('password')})});
  if (res.ok) { location.reload(); return; }
  document.getElementById('err').textContent = (await res.json()).error;
};
</script></body></html>`

// depthLimit rejects operations nested deeper than max selection levels.
type depthLimit int

var _ interface {
	graphql.HandlerExtension
	graphql.OperationContextMutator
} = depthLimit(0)

func (depthLimit) ExtensionName() string                   { return "DepthLimit" }
func (depthLimit) Validate(graphql.ExecutableSchema) error { return nil }
func (d depthLimit) MutateOperationContext(_ context.Context, rc *graphql.OperationContext) *gqlerror.Error {
	if rc.Operation == nil {
		return nil
	}
	if depth := selectionDepth(rc.Operation.SelectionSet, rc.Doc.Fragments, 0); depth > int(d) {
		return gqlerror.Errorf("operation depth %d exceeds the limit of %d", depth, int(d))
	}
	return nil
}

func selectionDepth(set ast.SelectionSet, fragments ast.FragmentDefinitionList, seen int) int {
	if seen > 64 {
		return seen
	}
	deepest := 0
	for _, sel := range set {
		var d int
		switch s := sel.(type) {
		case *ast.Field:
			d = 1 + selectionDepth(s.SelectionSet, fragments, seen+1)
		case *ast.InlineFragment:
			d = selectionDepth(s.SelectionSet, fragments, seen+1)
		case *ast.FragmentSpread:
			if def := fragments.ForName(s.Name); def != nil {
				d = selectionDepth(def.SelectionSet, fragments, seen+1)
			}
		}
		deepest = max(deepest, d)
	}
	return deepest
}
