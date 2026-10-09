// Package configmcp serves the configuration MCP endpoint at /admin/mcp: an
// agent, signed in with one of an administrator's tokens, searches the
// scanned catalog, scans schemas, previews plans and saves them as drafts,
// and validates, diffs and simulates configurations. It has no tool to
// publish: an administrator reviews and publishes drafts in the console. Each
// tool calls the admin API resolvers, so permissions and results are the
// console's.
package configmcp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/version"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/graph"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// Tokens finds and records the use of administrators' tokens.
type Tokens interface {
	AdminTokenByHash(ctx context.Context, hash string) (configstore.AdminToken, bool, error)
	TouchAdminToken(ctx context.Context, id string, at time.Time) error
}

// Options configure the endpoint.
type Options struct {
	// PathPrefix is the gateway prefix the console is served under.
	PathPrefix string
	Now        func() time.Time
}

// touchEvery bounds how often a token's last use is written.
const touchEvery = time.Minute

// New returns the endpoint's handler. Requests authenticate with
// Authorization: Bearer <token>, a token an administrator generated in the
// console for one client; it acts with the administrator's current
// permissions and stops working when it expires, is revoked or the account
// is disabled.
func New(r *graph.Resolver, tokens Tokens, admins accounts.Store, opts Options) http.Handler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := mcp.NewServer(
		&mcp.Implementation{Name: "sql-mcp-server-config", Version: version.String()},
		&mcp.ServerOptions{
			Instructions: instructions,
		},
	)
	register(s, r)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return authenticate(tokens, admins, opts, handler)
}

const instructions = `Configures sql-mcp-server for an administrator. Find tables with catalog_search (scan
schemas with scan_schemas when they are not scanned), describe what to do as a plan, check it with
plan_preview (a summary; page through its objects and exceptions with plan_details) and save it with
plan_save_draft, passing the planId plan_preview returned; continue a draft by passing it as base.
An administrator reviews and publishes drafts in the console (the result links to it). A plan is
{scope, import, grants, fields, sync, exclude, names}: scope selects scanned tables by datasource,
schema and table glob patterns, search text and status (new, workspace, published); import adds
zero-permission entities for new tables; grants grant actions (read, aggregate, create, update,
delete) to role:<name> or user:<name>, optionally limited to fields and rows; fields hide (exclude)
or mask columns matching patterns; sync adds and removes fields to match the columns. Exceptions of
level block must be resolved and those of level confirm reported to the administrator.`

type ctxKey int

const (
	baseURLKey ctxKey = iota
	tokenKey
)

// requestBase is where the console is reached, as the request came.
func requestBase(r *http.Request, prefix string) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host + prefix
}

func authenticate(tokens Tokens, admins accounts.Store, opts Options, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			unauthorized(w)
			return
		}
		found, ok, err := tokens.AdminTokenByHash(r.Context(), config.TokenHash(strings.TrimSpace(token)))
		if err != nil {
			slog.Error("config MCP token lookup failed", "error", err.Error())
			http.Error(w, "token lookup failed", http.StatusInternalServerError)
			return
		}
		now := opts.Now()
		if !ok || !found.ExpiresAt.IsZero() && !now.Before(found.ExpiresAt) {
			unauthorized(w)
			return
		}
		account, err := admins.GetAdmin(r.Context(), found.Username)
		if err != nil || account.Disabled {
			unauthorized(w)
			return
		}
		if now.Sub(found.LastUsedAt) > touchEvery {
			if err := tokens.TouchAdminToken(r.Context(), found.ID, now); err != nil {
				slog.Warn("config MCP token use not recorded", "error", err.Error())
			}
		}
		ctx := auth.WithPrincipal(
			r.Context(),
			auth.Principal{Username: account.Username, Permissions: account.Permissions},
		)
		ctx = context.WithValue(ctx, baseURLKey, requestBase(r, opts.PathPrefix))
		ctx = context.WithValue(ctx, tokenKey, found.Name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="sql-mcp-server admin"`)
	http.Error(w, "an administrator's MCP token is required", http.StatusUnauthorized)
}
