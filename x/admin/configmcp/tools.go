package configmcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/graph"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

type catalogIn struct {
	graph.CatalogFilter
	Offset int `json:"offset,omitempty" jsonschema:"tables to skip"`
	Limit  int `json:"limit,omitempty"  jsonschema:"tables to return, at most 500 (default 50)"`
}

type refIn struct {
	Ref string `json:"ref" jsonschema:"datasource.schema.table, as catalog_search returns it"`
}

type scopeIn struct {
	Scope configplan.Scope `json:"scope" jsonschema:"the scope part of a plan"`
}

type validateIn struct {
	Draft graph.DraftInput `json:"draft" jsonschema:"a base revision and the sections to replace"`
}

type diffIn struct {
	From   string `json:"from"             jsonschema:"revision ID"`
	To     string `json:"to,omitempty"     jsonschema:"revision ID (default: the published one)"`
	Offset int    `json:"offset,omitempty" jsonschema:"lines to skip"`
	Limit  int    `json:"limit,omitempty"  jsonschema:"lines to return, at most 2000 (default 400)"`
}

type revisionsIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"revisions to list, newest first (default 20)"`
}

type datasourceIn struct {
	Datasource string `json:"datasource"`
}

type none struct{}

// revisionOut is a revision without its configuration.
type revisionOut struct {
	ID        string `json:"id"`
	Parent    string `json:"parent,omitempty"`
	State     string `json:"state"`
	Author    string `json:"author"`
	Comment   string `json:"comment"`
	DiffLines int    `json:"diffLines,omitempty"`
	ReviewURL string `json:"reviewURL,omitempty"`
}

func toRevisionOut(r graph.Revision) revisionOut {
	out := revisionOut{ID: r.ID, State: string(r.State), Author: r.Author, Comment: r.Comment}
	if r.Parent != nil {
		out.Parent = *r.Parent
	}
	return out
}

// planJSON passes a plan to the admin API as its JSON value.
func planJSON(p any) (any, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var v any
	return v, json.Unmarshal(data, &v)
}

func tool(name, description string, readOnly bool) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}}
}

// add registers a tool whose calls are logged with the administrator and
// the token's client.
func add[In any](s *mcp.Server, t *mcp.Tool, fn func(context.Context, In) (any, error)) {
	mcp.AddTool(s, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		out, err := fn(ctx, in)
		who, _ := auth.PrincipalFrom(ctx)
		client, _ := ctx.Value(tokenKey).(string)
		attrs := []any{"tool", t.Name, "username", who.Username, "client", client}
		if err != nil {
			slog.Info("config MCP tool failed", append(attrs, "error", err.Error())...)
			return nil, nil, err
		}
		slog.Info("config MCP tool", attrs...)
		return nil, out, nil
	})
}

// reviewURL is where an administrator reviews a revision in the console:
// next to server.publicURL when configured, otherwise where the request
// came.
func reviewURL(ctx context.Context, r *graph.Resolver, id string) string {
	base, _ := ctx.Value(baseURLKey).(string)
	if rev, err := r.Store.Published(ctx); err == nil {
		if cfg, err := bootstrap.LoadRevision(rev); err == nil && cfg.Server.PublicURL != "" {
			base = strings.TrimSuffix(strings.TrimSuffix(cfg.Server.PublicURL, "/"), "/mcp")
		}
	}
	return base + "/admin/revisions?id=" + id
}

func register(s *mcp.Server, r *graph.Resolver) {
	registerCatalog(s, r)
	registerScans(s, r)
	registerPlans(s, r)
	registerReads(s, r)
}

func registerCatalog(s *mcp.Server, r *graph.Resolver) {
	q := r.Query()
	add(s, tool("catalog_search", "Search the scanned tables of every datasource, a page at a time, with their "+
		"status against the published configuration, counts by status and what is scanned of each datasource.", true),
		func(ctx context.Context, in catalogIn) (any, error) {
			limit := in.Limit
			if limit == 0 {
				limit = 50
			}
			return q.Catalog(ctx, graph.CatalogInput{Filter: &in.CatalogFilter, Offset: &in.Offset, Limit: &limit})
		})
	add(s, tool("catalog_table", "One scanned table in full: columns, keys, indexes, foreign keys and the entity "+
		"proposed for it.", true),
		func(ctx context.Context, in refIn) (any, error) { return q.CatalogTable(ctx, in.Ref, nil) })
	add(s, tool("catalog_links", "The foreign keys of a scanned table and those of other tables referencing it.", true),
		func(ctx context.Context, in refIn) (any, error) { return q.CatalogLinks(ctx, in.Ref, nil) })
	add(s, tool("plan_scope", "How many tables a plan scope selects, the first few, and how many each condition "+
		"selects alone: check a scope before writing rules.", true),
		func(ctx context.Context, in scopeIn) (any, error) {
			scope, err := planJSON(in.Scope)
			if err != nil {
				return nil, err
			}
			return q.PlanScope(ctx, scope, nil)
		})
}

func registerReads(s *mcp.Server, r *graph.Resolver) {
	q := r.Query()
	add(s, tool("plan_templates", "The saved plan templates.", true),
		func(ctx context.Context, _ none) (any, error) { return q.PlanTemplates(ctx) })
	add(s, tool("pending", "What waits for review: recurring templates with new matches or rules they cannot "+
		"apply, tables whose fields differ from their columns, tables not imported and schemas never scanned.", true),
		func(ctx context.Context, _ none) (any, error) { return q.Pending(ctx, nil) })
	add(s, tool("validate", "Validate a configuration draft without saving it.", true),
		func(ctx context.Context, in validateIn) (any, error) { return q.Validate(ctx, in.Draft) })
	add(s, tool("diff", "Unified diff between two revisions, a page of lines at a time.", true),
		func(ctx context.Context, in diffIn) (any, error) {
			var to *string
			if in.To != "" {
				to = &in.To
			}
			out, err := q.Diff(ctx, in.From, to, nil)
			if err != nil {
				return nil, err
			}
			return diffPage(out, in.Offset, in.Limit), nil
		})
	add(s, tool("simulate", "Evaluate one authorization offline against a revision (default: the published "+
		"one): whether a user or role:<name> may run an action on an entity, with which fields and rows.", true),
		func(ctx context.Context, in graph.SimulationInput) (any, error) { return q.Simulate(ctx, in) })
	add(s, tool("revisions", "Revisions, newest first, without their configuration.", true),
		func(ctx context.Context, in revisionsIn) (any, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 20
			}
			revs, err := q.Revisions(ctx, &limit)
			out := make([]revisionOut, 0, len(revs))
			for _, rev := range revs {
				out = append(out, toRevisionOut(rev))
			}
			return out, err
		})
}

// diffPage is a page of a diff's lines.
func diffPage(diff string, offset, limit int) map[string]any {
	lines := strings.SplitAfter(diff, "\n")
	if limit <= 0 {
		limit = 400
	}
	limit = min(limit, 2000)
	offset = min(max(offset, 0), len(lines))
	end := min(offset+limit, len(lines))
	return map[string]any{"total": len(lines), "offset": offset, "diff": strings.Join(lines[offset:end], "")}
}
