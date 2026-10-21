// Package configmcp serves the configuration MCP endpoint at /admin/mcp: an
// agent, signed in with one of an administrator's tokens, acts for the
// administrator: it searches the scanned catalog, scans schemas, previews
// plans against the administrator's workspace and writes them into it (the
// one copy the administrator edits in the console, so they see the changes
// as if made there), and diffs and simulates configurations. It has no tool to publish: the administrator
// publishes in the console. Each tool calls the configuration service the
// console's API calls, so permissions, checks and results are the console's;
// a token may be limited to some datasources.
package configmcp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/version"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// Service is what the endpoint may do with the configuration service: read
// the catalog, revisions and pending work, scan schemas, preview plans and
// apply them to the administrator's workspace, diff and simulate. Nothing
// else of configapp.Service (publishing, tokens, the workspace's versioning
// operations, saving templates) is reachable from an agent's token: the type
// says so, not a review.
type Service interface {
	ApplyPlan(ctx context.Context, in configapp.ApplyRequest) (*configapp.AppliedPlan, error)
	Catalog(ctx context.Context, f configapp.CatalogFilter, against configapp.Against, offset, limit int) (
		*configapp.CatalogPage, error)
	CatalogLinks(ctx context.Context, ref string, against configapp.Against) (*configapp.CatalogLinks, error)
	CatalogTable(ctx context.Context, ref string, against configapp.Against) (*configplan.Candidate, error)
	Diff(ctx context.Context, from string, to configapp.Against) (string, error)
	Pending(ctx context.Context, against configapp.Against) (*configapp.PendingWork, error)
	PlanScope(ctx context.Context, scope configplan.Scope, against configapp.Against) (*configapp.ScopeStats, error)
	PlanTemplates(ctx context.Context) ([]configapp.PlanTemplate, error)
	PreviewInWorkspace(ctx context.Context, p configplan.Plan, review bool) (*configapp.Preview, error)
	Previewed(ctx context.Context, planID string) (*configapp.Preview, error)
	Revisions(ctx context.Context, limit int) ([]revision.Revision, error)
	ScanBatches(ctx context.Context, datasource string) ([]configapp.ScanBatch, error)
	SchemaList(ctx context.Context, datasource string, refresh bool) (*configapp.SchemaList, error)
	Simulate(ctx context.Context, in configapp.SimulateRequest) (*configapp.Simulation, error)
	StartScanBatch(ctx context.Context, in configapp.ScanBatchInput) (*configapp.ScanBatch, error)
}

var _ Service = (*configapp.Service)(nil)

// Tokens finds and records the use of administrators' tokens.
type Tokens interface {
	AdminTokenByHash(ctx context.Context, hash string) (configstore.AdminToken, bool, error)
	TouchAdminToken(ctx context.Context, id string, at time.Time) error
}

// Options configure the endpoint.
type Options struct {
	Now func() time.Time
}

// touchEvery bounds how often a token's last use is written.
const touchEvery = time.Minute

// New returns the endpoint's handler. Requests authenticate with
// Authorization: Bearer <token>, a token an administrator generated in the
// console for one client; it acts with the administrator's current
// permissions and stops working when it expires, is revoked or the account
// is disabled.
func New(app Service, tokens Tokens, admins accounts.Store, opts Options) http.Handler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := mcp.NewServer(
		&mcp.Implementation{Name: "sql-mcp-server-config", Version: version.String()},
		&mcp.ServerOptions{
			Instructions: instructions,
		},
	)
	register(s, app)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return authenticate(tokens, admins, opts, handler)
}

const instructions = `Configures sql-mcp-server for an administrator. Find tables with catalog_search (scan
schemas with scan_schemas when they are not scanned), describe what to do as a plan, check it with
plan_preview against the administrator's workspace (a summary; page through what it previewed with
plan_details and its planId) and apply it with plan_apply, passing the planId and the IDs of the
exceptions to confirm the administrator confirmed. You act for the administrator: you write the workspace
they edit in the console, as if they made the changes; they keep, edit or revert each and publish.
A plan is
{scope, import, grants, fields, sync, exclude, names}: scope selects scanned tables by datasource,
schema and table glob patterns, search text and status (new, workspace, published); import adds
zero-permission entities for new tables; grants grant actions (read, aggregate, create, update,
delete) to role:<name> or user:<name>, optionally limited to fields and rows; fields hide (exclude)
or mask columns matching patterns; sync adds and removes fields to match the columns, and with
removeTables removes the entities whose tables are gone (scanned schemas without them, or found missing
by the serving configuration) with the grants and relationships naming them. Exceptions of
level block must be resolved and those of level confirm reported to the administrator.`

type ctxKey int

// tokenKey holds the name of the token (its client) on a request.
const tokenKey ctxKey = iota

func authenticate(tokens Tokens, admins accounts.Store, opts Options, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The scheme is case-insensitive (RFC 9110), as on /mcp.
		scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
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
		if !ok || !found.ExpiresAt.IsZero() && !now.Before(found.ExpiresAt) || found.Scope.ReadOnly {
			// A read-only token (no longer offered) is refused, never widened.
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
		ctx := auth.WithPrincipal(r.Context(), scoped(account.Username, account.Permissions, found.Scope))
		ctx = context.WithValue(ctx, tokenKey, found.Name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// scoped is an administrator as one of their tokens acts: with the
// administrator's current permissions, limited to the token's datasources.
func scoped(username string, permissions []string, scope configstore.TokenScope) auth.Principal {
	return auth.Principal{Username: username, Permissions: permissions, Datasources: scope.Datasources}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="sql-mcp-server admin"`)
	http.Error(w, "an administrator's MCP token is required", http.StatusUnauthorized)
}
