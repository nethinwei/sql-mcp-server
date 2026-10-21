package configmcp

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

type catalogIn struct {
	configapp.CatalogFilter
	Offset int `json:"offset,omitempty" jsonschema:"tables to skip"`
	Limit  int `json:"limit,omitempty"  jsonschema:"tables to return, at most 500 (default 50)"`
}

type refIn struct {
	Ref string `json:"ref" jsonschema:"datasource.schema.table, as catalog_search returns it"`
}

type scopeIn struct {
	Scope configplan.Scope `json:"scope" jsonschema:"the scope part of a plan"`
}

type diffIn struct {
	From   string `json:"from"             jsonschema:"revision ID"`
	To     string `json:"to,omitempty"     jsonschema:"revision ID (default: the published one)"`
	Offset int    `json:"offset,omitempty" jsonschema:"lines to skip"`
	Limit  int    `json:"limit,omitempty"  jsonschema:"lines to return, at most 2000 (default 400)"`
}

type simulateIn struct {
	Revision string         `json:"revision,omitempty" jsonschema:"revision ID (default: the published one)"`
	User     string         `json:"user"               jsonschema:"a configured user, or role:<name>"`
	Entity   string         `json:"entity"`
	Action   string         `json:"action"             jsonschema:"read, aggregate, create, update, delete or execute"`
	Fields   []string       `json:"fields,omitempty"   jsonschema:"fields to request; empty: the default projection"`
	Subject  map[string]any `json:"subject,omitempty"  jsonschema:"attributes the user does not set"`
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
	ID      string `json:"id"`
	Parent  string `json:"parent,omitempty"`
	State   string `json:"state"`
	Author  string `json:"author"`
	Comment string `json:"comment"`
}

// agentsRead is what agents read, but for plans (preview and apply), which
// they make in their administrator's workspace.
var agentsRead = configapp.Against{}

func toRevisionOut(r revision.Revision) revisionOut {
	state := strings.ToUpper(strings.ReplaceAll(string(r.State), "-", "_"))
	out := revisionOut{ID: id(r.ID), State: state, Author: r.Author, Comment: r.Comment}
	if r.ParentID != 0 {
		out.Parent = id(r.ParentID)
	}
	return out
}

func id(n int64) string { return strconv.FormatInt(n, 10) }

// columnOut and tableOut show a scanned table and the entity proposed for it.
type columnOut struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Nullable    bool   `json:"nullable,omitempty"`
	PrimaryKey  bool   `json:"primaryKey,omitempty"`
	Description string `json:"description,omitempty"`
}

type tableOut struct {
	Ref          string              `json:"ref"`
	Description  string              `json:"description,omitempty"`
	Columns      []columnOut         `json:"columns"`
	Keys         []entity.Key        `json:"keys,omitempty"`
	Indexes      []entity.Index      `json:"indexes,omitempty"`
	ForeignKeys  []entity.ForeignKey `json:"foreignKeys,omitempty"`
	SideEffects  bool                `json:"sideEffects,omitempty"`
	ConfiguredAs string              `json:"configuredAs,omitempty"`
	// Candidate is the entity proposed for the table; ID its identity.
	ID        string `json:"id"`
	Candidate any    `json:"candidate"`
}

func toTableOut(ref string, c configplan.Candidate) tableOut {
	t := c.Table
	pk := t.PrimaryKey()
	out := tableOut{Ref: ref, Description: t.Description, Keys: t.Keys, Indexes: t.Indexes, ForeignKeys: t.ForeignKeys,
		SideEffects: t.SideEffects, ID: c.ID, Candidate: c.Entity, Columns: make([]columnOut, 0, len(t.Attributes))}
	for _, a := range t.Attributes {
		out.Columns = append(out.Columns, columnOut{Name: a.Name, Type: a.Domain.Type, Nullable: a.Domain.Nullable,
			PrimaryKey: slices.Contains(pk, a.Name), Description: a.Description})
	}
	if c.Configured != nil {
		out.ConfiguredAs = c.Configured.Name
	}
	return out
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

func register(s *mcp.Server, app Service) {
	registerCatalog(s, app)
	registerScans(s, app)
	registerPlans(s, app)
	registerReads(s, app)
}

func registerCatalog(s *mcp.Server, app Service) {
	add(s, tool("catalog_search", "Search the scanned tables of every datasource (the token's), a page at a time, "+
		"with their status against the published configuration, counts by status and what is scanned of each "+
		"datasource.", true),
		func(ctx context.Context, in catalogIn) (any, error) {
			limit := in.Limit
			if limit == 0 {
				limit = 50
			}
			return app.Catalog(ctx, in.CatalogFilter, agentsRead, in.Offset, limit)
		})
	add(s, tool("catalog_table", "One scanned table in full: columns, keys, indexes, foreign keys and the entity "+
		"proposed for it.", true),
		func(ctx context.Context, in refIn) (any, error) {
			c, err := app.CatalogTable(ctx, in.Ref, agentsRead)
			if err != nil || c == nil {
				return nil, err
			}
			return toTableOut(in.Ref, *c), nil
		})
	add(s, tool("catalog_links", "The foreign keys of a scanned table and those of other tables referencing it.", true),
		func(ctx context.Context, in refIn) (any, error) { return app.CatalogLinks(ctx, in.Ref, agentsRead) })
	add(s, tool("plan_scope", "How many tables a plan scope selects, the first few, and how many each condition "+
		"selects alone: check a scope before writing rules.", true),
		func(ctx context.Context, in scopeIn) (any, error) { return app.PlanScope(ctx, in.Scope, agentsRead) })
}

func registerReads(s *mcp.Server, app Service) {
	add(s, tool("plan_templates", "The saved plan templates.", true),
		func(ctx context.Context, _ none) (any, error) { return app.PlanTemplates(ctx) })
	add(s, tool("pending", "What waits for review: recurring templates with new matches or rules they cannot "+
		"apply, tables whose fields differ from their columns or are gone (up to 200 each, unhandled first; "+
		"moreDriftTables/moreGoneTables count the rest), tables not imported and schemas never scanned.", true),
		func(ctx context.Context, _ none) (any, error) { return app.Pending(ctx, agentsRead) })
	add(s, tool("diff", "Unified diff between two revisions, a page of lines at a time; secrets are redacted.", true),
		func(ctx context.Context, in diffIn) (any, error) {
			var to configapp.Against
			if in.To != "" {
				to.Revision = &in.To
			}
			out, err := app.Diff(ctx, in.From, to)
			if err != nil {
				return nil, err
			}
			return diffPage(out, in.Offset, in.Limit), nil
		})
	add(s, tool("simulate", "Evaluate one authorization offline against a revision (default: the published "+
		"one): whether a user or role:<name> may run an action on an entity, with which fields and rows.", true),
		func(ctx context.Context, in simulateIn) (any, error) {
			req := configapp.SimulateRequest{User: in.User, Entity: in.Entity, Action: in.Action, Fields: in.Fields,
				Subject: in.Subject}
			if in.Revision != "" {
				req.Against.Revision = &in.Revision
			}
			return app.Simulate(ctx, req)
		})
	add(s, tool("revisions", "Revisions, newest first, without their configuration.", true),
		func(ctx context.Context, in revisionsIn) (any, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 20
			}
			revs, err := app.Revisions(ctx, limit)
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
	if len(lines) > 0 && lines[len(lines)-1] == "" { // after the last newline: no line
		lines = lines[:len(lines)-1]
	}
	if limit <= 0 {
		limit = 400
	}
	limit = min(limit, 2000)
	offset = min(max(offset, 0), len(lines))
	end := min(offset+limit, len(lines))
	return map[string]any{"total": len(lines), "offset": offset, "diff": strings.Join(lines[offset:end], "")}
}
