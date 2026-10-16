package mcpserver

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/tool"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// The schema resource summarizes what the caller may read; each entity's
// fields and access are a resource of their own, so neither grows with the
// number of entities a deployment exposes.
const (
	schemaResourceURI    = "sql-mcp://schema"
	schemaEntityPrefix   = schemaResourceURI + "/"
	schemaEntityTemplate = schemaEntityPrefix + "{entity}"
	schemaUsage          = "Find entities with describe_entities (search, datasource, offset, limit), " +
		"then read " + schemaEntityTemplate + " for an entity's fields, keys and per-action access."
)

func registerSchemaResource(s *mcp.Server, acquire appAcquire) {
	s.AddResource(&mcp.Resource{
		URI:         schemaResourceURI,
		Name:        "authorized-schema",
		Title:       "Authorized SQL schema",
		Description: "How many entities the caller may read, per datasource, and how to look them up",
		MIMEType:    "application/json",
	}, schemaHandler(acquire, func(ctx context.Context, app *bootstrap.App, role string, subject map[string]any,
		_ string) (map[string]any, bool, error) {
		summary, err := schemaSummary(ctx, app, role, subject)
		return summary, true, err
	}))
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: schemaEntityTemplate,
		Name:        "authorized-entity",
		Title:       "Authorized entity schema",
		Description: "One entity's fields, keys and per-action access visible to the caller",
		MIMEType:    "application/json",
	}, schemaHandler(acquire, func(ctx context.Context, app *bootstrap.App, role string, subject map[string]any,
		uri string) (map[string]any, bool, error) {
		ref, ok := strings.CutPrefix(uri, schemaEntityPrefix)
		if !ok || ref == "" {
			return nil, false, nil
		}
		return entitySchema(ctx, app, role, subject, ref)
	}))
}

type schemaRead func(ctx context.Context, app *bootstrap.App, role string, subject map[string]any,
	uri string) (map[string]any, bool, error)

// schemaHandler serves read as JSON for the caller; a resource the caller
// may not read is reported as not found, like one that does not exist.
func schemaHandler(acquire appAcquire, read schemaRead) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		app, release, err := acquire()
		if err != nil {
			return nil, err
		}
		defer release()
		role, subject := callerIdentity(ctx, app)
		payload, ok, err := read(ctx, app, role, subject, req.Params.URI)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: req.Params.URI, MIMEType: "application/json", Text: string(data),
		}}}, nil
	}
}

// schemaSummary counts the entities role may read or aggregate, per datasource.
func schemaSummary(
	ctx context.Context, app *bootstrap.App, role string, subject map[string]any,
) (map[string]any, error) {
	counts := map[string]int{}
	total := 0
	for e := range app.Registry.All() {
		_, _, ok, err := entityActions(ctx, app, e, role, subject)
		if err != nil {
			return nil, err
		}
		if ok {
			counts[e.DatasourceName()]++
			total++
		}
	}
	datasources := make([]map[string]any, 0, len(counts))
	for name, n := range counts {
		datasources = append(datasources, map[string]any{"name": name, "entities": n})
	}
	sort.Slice(datasources, func(i, j int) bool {
		return datasources[i]["name"].(string) < datasources[j]["name"].(string)
	})
	return map[string]any{"role": role, "entities": total, "datasources": datasources, "usage": schemaUsage}, nil
}

// entitySchema describes the one entity ref names among those role may use.
func entitySchema(
	ctx context.Context, app *bootstrap.App, role string, subject map[string]any, ref string,
) (map[string]any, bool, error) {
	usable := tool.UsableBy(ctx, app.Authorizer, role, subject)
	var found []entity.Entity
	for _, e := range app.Registry.Match(ref) {
		if usable(e) {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		return nil, false, nil
	}
	entry, ok, err := authorizedEntity(ctx, app, found[0], role, subject)
	if ok {
		entry["name"] = app.Registry.ShortName(found[0], usable)
	}
	return entry, ok, err
}

// entityActions is what role may read and aggregate of e.
func entityActions(
	ctx context.Context, app *bootstrap.App, e entity.Entity, role string, subject map[string]any,
) ([]string, map[entity.Action]entityAccess, bool, error) {
	if e.Kind == entity.KindProcedure || !e.MCP.DMLTools {
		return nil, nil, false, nil
	}
	var actions []string
	access := map[entity.Action]entityAccess{}
	for _, action := range []entity.Action{entity.ActionRead, entity.ActionAggregate} {
		dec, err := app.Authorizer.Authorize(ctx, rbac.Request{
			Role: role, Subject: subject, Entity: e.Name, Action: action,
		})
		if err != nil {
			return nil, nil, false, err
		}
		if a := accessOf(e, dec); a.allowed {
			actions = append(actions, action.String())
			access[action] = a
		}
	}
	return actions, access, len(actions) > 0, nil
}

func authorizedEntity(
	ctx context.Context,
	app *bootstrap.App,
	e entity.Entity,
	role string,
	subject map[string]any,
) (map[string]any, bool, error) {
	// Read and aggregate grants may cover different fields, so each action
	// reports its own fields and scopes; top-level fields describe them all.
	actions, byAction, ok, err := entityActions(ctx, app, e, role, subject)
	if !ok || err != nil {
		return nil, false, err
	}
	access := map[string]any{}
	union := map[string]bool{}
	for action, a := range byAction {
		access[action.String()] = a.describe()
		for _, f := range a.fields {
			union[f] = true
		}
	}
	return map[string]any{
		"name": e.Name, "id": e.Name, "description": e.Description, "datasource": e.DatasourceName(),
		"fields":    authorizedEntityFields(e, e.OrderedNames(union)),
		"keys":      tool.VisibleKeys(e, e.OrderedNames(union)),
		"indexes":   tool.VisibleIndexes(e, e.OrderedNames(union)),
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
		safeReadPrompt = "Find the entity with describe_entities (search by keyword) and read its " +
			schemaEntityTemplate + " resource first. Call only read_records, " +
			"select only fields listed in access.read (all within one access.read.fieldScopes entry when " +
			"explicitFieldsRequired is set), " +
			"add the narrowest supported filter, and set a conservative limit. " +
			"Never invent entities or fields."
		safeAggregatePrompt = "Find the entity with describe_entities (search by keyword) and read its " +
			schemaEntityTemplate + " resource first. Call only aggregate_records " +
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
