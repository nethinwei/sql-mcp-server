// Package admin serves the admin API under /admin: sign-in with local
// accounts, cookie sessions with CSRF tokens, the GraphQL endpoint, and the
// configuration MCP endpoint at /admin/mcp for administrators' tokens.
package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/admin/configmcp"
	"github.com/nethinwei/sql-mcp-server/x/admin/graph"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/admin/ui"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
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
	Store    revision.Store
	Accounts accounts.Store
	// Catalog keeps what schema scans found; in memory when nil.
	Catalog catalog.Store
	// Plans keeps plan templates and MCP tokens; nil refuses them.
	Plans      configapp.PlanStore
	Introspect configapp.Introspection
	// Bus runs the scans and the console's event feed; optional (unbounded
	// without one).
	Bus *bus.Bus
	// Status reports the serving process to the console; optional.
	Status func() graph.RuntimeState
	// Capabilities reports what the serving connections may do; optional.
	Capabilities func() bootstrap.EntityCapabilities
	// Profiles reports what the serving connections guarantee; optional.
	Profiles func() ([]bootstrap.ConnectionProfile, []bootstrap.LimitIssue)
	// Missing reports what the serving configuration found missing from the
	// databases; optional.
	Missing func() bootstrap.Missing
	// PlannedLimits judges a configuration before it is applied; optional.
	PlannedLimits func(*config.Config) []bootstrap.LimitIssue
	// PathPrefix is the path a gateway serves the service under and strips
	// from requests (server.pathPrefix); pages, the cookie and redirects
	// carry it.
	PathPrefix string
	// SecureCookie marks the session cookie Secure; set it when serving TLS.
	SecureCookie bool
	// Playground mounts GraphiQL at /admin/playground and enables GraphQL
	// schema introspection. It is a development aid.
	Playground bool
	Now        func() time.Time
}

// Handler serves /admin/*.
type Handler struct {
	cfg       Config
	sessions  *sessions
	logins    *loginGate
	graphql   http.Handler
	mux       *http.ServeMux
	scans     *configapp.Scans
	published *publishedFeed
	// workspaces is nil when no store keeps workspaces.
	workspaces *workspaceFeed
	// ending is closed when the event streams end (EndStreams).
	ending     chan struct{}
	endStreams sync.Once
}

// New builds the admin handler.
func New(cfg Config) *Handler {
	h := &Handler{
		cfg: cfg, sessions: newSessions(cfg.Now), logins: newLoginGate(maxConcurrentLogins), ending: make(chan struct{}),
		scans:     configapp.NewScans(scanjobs.Options{Bus: cfg.Bus}, cfg.Catalog),
		published: newPublishedFeed(cfg.Bus, cfg.Store, eventPollInterval),
	}
	if cfg.Plans != nil {
		h.workspaces = newWorkspaceFeed(cfg.Bus, h.workspaceVersion, eventPollInterval)
	}
	app := &configapp.Service{
		Store: cfg.Store, Introspect: cfg.Introspect, Capabilities: cfg.Capabilities, Profiles: cfg.Profiles,
		Missing: cfg.Missing, PlannedLimits: cfg.PlannedLimits, Scans: h.scans, Plans: cfg.Plans,
	}
	app.WatchScanBatches()
	cfg.Bus.Go("warm console caches", app.Warm)
	resolver := &graph.Resolver{App: app, Accounts: accounts.Service{Store: cfg.Accounts}, Status: cfg.Status}
	srv := handler.New(graph.NewExecutableSchema(graph.Config{Resolvers: resolver}))
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
	h.mux.Handle("GET /admin/", ui.Handler("/admin/", h.path("/admin/")))
	h.mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, h.path("/admin/"), http.StatusFound)
	})
	h.mux.HandleFunc("POST /admin/logout", h.logout)
	h.mux.Handle("POST /admin/graphql", compressed(h.authenticated(true, h.graphql)))
	h.mux.HandleFunc("GET /admin/events", h.events)
	h.mux.HandleFunc("GET /admin/graphql", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GraphQL accepts POST only"})
	})
	if cfg.Playground {
		h.mux.HandleFunc("GET /admin/playground", h.playground)
	}
	if cfg.Plans != nil {
		opts := configmcp.Options{Now: cfg.Now}
		mcpHandler := configmcp.New(app, cfg.Plans, cfg.Accounts, opts)
		for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
			h.mux.Handle(method+" /admin/mcp", mcpHandler)
		}
	}
	return h
}

// path is where browsers reach p, a path of this service, through the
// gateway prefix.
func (h *Handler) path(p string) string { return h.cfg.PathPrefix + p }

// Close stops the background schema scans.
func (h *Handler) Close() {
	h.scans.Close()
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
		Name: cookieName, Value: id, Path: h.path("/admin"), HttpOnly: true, Secure: h.cfg.SecureCookie,
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
	sess, principal, err := h.session(r)
	if err != nil {
		refuseSession(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": principal.Username, "permissions": principal.Permissions, "csrfToken": sess.csrf,
	})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		if sess, ok := h.sessions.lookup(c.Value); ok && sameToken(r.Header.Get(csrfHeader), sess.csrf) {
			h.sessions.delete(c.Value)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Path: h.path("/admin"), HttpOnly: true, Secure: h.cfg.SecureCookie,
		SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// credentialTag fingerprints a password hash for comparison with a session.
func credentialTag(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:])
}

// sameToken compares a token a request presents with the one expected, in
// constant time.
func sameToken(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// errSignedOut: the request has no session, or its account no longer holds
// it (deleted, disabled, or its password changed).
var errSignedOut = auth.ErrUnauthorized

// session resolves the request's session and re-reads its account, so a
// disabled or deleted account, or one whose password changed, loses access on
// its next request: errSignedOut, and the session is deleted. An account that
// cannot be read now (the store failing, the request ending) is another
// error, and the session stays.
func (h *Handler) session(r *http.Request) (session, auth.Principal, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, auth.Principal{}, errSignedOut
	}
	sess, ok := h.sessions.lookup(c.Value)
	if !ok {
		return session{}, auth.Principal{}, errSignedOut
	}
	principal, err := h.holder(r.Context(), sess)
	if errors.Is(err, errSignedOut) {
		h.sessions.delete(c.Value)
	}
	return sess, principal, err
}

// holder re-reads the account of sess: errSignedOut when it no longer holds
// the session.
func (h *Handler) holder(ctx context.Context, sess session) (auth.Principal, error) {
	account, err := h.cfg.Accounts.GetAdmin(ctx, sess.username)
	switch {
	case errors.Is(err, configstore.ErrAdminNotFound):
		return auth.Principal{}, errSignedOut
	case err != nil:
		return auth.Principal{}, err
	case account.Disabled || credentialTag(account.PasswordHash) != sess.credential:
		return auth.Principal{}, errSignedOut
	}
	return auth.Principal{Username: account.Username, Permissions: account.Permissions}, nil
}

// refuseSession answers a request whose session did not resolve: 401 when
// signed out, else 503, as the session may serve the next request.
func refuseSession(w http.ResponseWriter, err error) {
	if errors.Is(err, errSignedOut) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": auth.ErrUnauthorized.Error()})
		return
	}
	slog.Warn("admin session not resolved", "err", err)
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the account cannot be read now"})
}

// authenticated requires a session and, when checkCSRF is set, the session's
// CSRF token in X-CSRF-Token.
func (h *Handler) authenticated(checkCSRF bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, principal, err := h.session(r)
		if err != nil {
			refuseSession(w, err)
			return
		}
		if checkCSRF && !sameToken(r.Header.Get(csrfHeader), sess.csrf) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing or invalid " + csrfHeader})
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
}

func (h *Handler) playground(w http.ResponseWriter, r *http.Request) {
	sess, _, err := h.session(r)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, loginPage)
		return
	}
	// gqlgen appends the endpoint to the host, so it is the absolute path,
	// under the gateway prefix.
	playground.Handler("sql-mcp-server admin", h.path("/admin/graphql"),
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
  const res = await fetch('login', {method: 'POST', headers: {'Content-Type': 'application/json'},
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
