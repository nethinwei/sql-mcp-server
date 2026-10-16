package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

// ---- describe_entities ----

// DescribeTool lists entities or one entity's fields.
type DescribeTool struct{}

// Info implements Tool.
func (DescribeTool) Info() Info {
	return Info{
		Name:   "describe_entities",
		Title:  "Describe entities",
		Action: "describe",
		Description: "Find the entities (tables and views) you may use and what you may do with them. " +
			"Without entity it lists one page of entities: narrow it with search (matches name, ID or " +
			"description) and datasource, and pass nextOffset as offset for the next page. With entity it " +
			"returns that entity's fields (type, required, readOnly), identity keys and datasource. " +
			"Use the names it returns in the other tools; never guess entity or field names.",
		InputSchema: schemaDescribe,
		ReadOnly:    true, Idempotent: true, Object: true,
	}
}

// Enabled implements Tool.
func (DescribeTool) Enabled(f config.ToolFlags) bool { return f.DescribeEntities }

// Listing bounds: a page holds describeDefaultLimit entities unless the
// caller asks for up to describeMaxLimit, so thousands of entities never come
// back in one result.
const (
	describeDefaultLimit = 100
	describeMaxLimit     = 500
)

type describeInput struct {
	Entity     string `json:"entity"`
	Search     string `json:"search"`
	Datasource string `json:"datasource"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
}

// Run implements Tool.
func (DescribeTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in describeInput
	if len(input) > 0 {
		if err := decodeInput(input, &in); err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
	}
	if in.Entity != "" {
		return describeEntity(ctx, tc, in.Entity)
	}
	if in.Offset < 0 || in.Limit < 0 || in.Limit > describeMaxLimit {
		return Result{}, fmt.Errorf("%w: offset must be >= 0 and limit between 1 and %d", ErrInvalidInput, describeMaxLimit)
	}
	if in.Limit == 0 {
		in.Limit = describeDefaultLimit
	}
	return Result{Content: []map[string]any{listEntities(ctx, tc, in)}}, nil
}

// listEntities is one page of the entities the caller may use whose name, ID
// or description contains search (ignoring case) and that are in datasource.
func listEntities(ctx context.Context, tc Context, in describeInput) map[string]any {
	visible := UsableBy(ctx, tc.Authorizer, tc.Role, tc.Subject)
	search := strings.ToLower(in.Search)
	page := []map[string]any{}
	total := 0
	for e := range tc.Registry.All() {
		if !e.MCP.DMLTools || (in.Datasource != "" && e.DatasourceName() != in.Datasource) || !visible(e) {
			continue
		}
		name := entityName(tc, e, visible)
		if search != "" && !strings.Contains(strings.ToLower(name+"\x00"+e.Name+"\x00"+e.Description), search) {
			continue
		}
		if total >= in.Offset && len(page) < in.Limit {
			page = append(page, map[string]any{
				"name": name, "id": e.Name, "description": e.Description, "datasource": e.DatasourceName(),
			})
		}
		total++
	}
	out := map[string]any{"entities": page, "total": total}
	if next := in.Offset + len(page); next < total {
		out["nextOffset"] = next
	}
	return out
}

// describeEntity lists the fields of one entity visible to the caller.
func describeEntity(ctx context.Context, tc Context, name string) (Result, error) {
	res, err := resolveDMLEntity(tc, name)
	if err != nil {
		return Result{}, err
	}
	fieldNames, allowed, err := describeFields(ctx, tc, res.Entity)
	if err != nil {
		return Result{}, err
	}
	if !allowed {
		return Result{}, ErrUnauthorized
	}
	fields := make([]map[string]any, 0, len(fieldNames))
	for _, name := range fieldNames {
		a, ok := res.Entity.AttributeByName(name)
		if !ok || a.Excluded {
			continue
		}
		fields = append(fields, map[string]any{
			"name": a.Name, "alias": a.Alias,
			"description": a.Description, "type": a.Domain.Type,
			"required": a.Domain.Required(), "readOnly": !a.Domain.Writable(),
		})
	}
	visible := UsableBy(ctx, tc.Authorizer, tc.Role, tc.Subject)
	return Result{Content: []map[string]any{{
		"name": entityName(tc, res.Entity, visible), "id": res.Entity.Name, "description": res.Entity.Description,
		"datasource": res.Entity.DatasourceName(), "fields": fields,
		"keys": VisibleKeys(res.Entity, fieldNames), "indexes": VisibleIndexes(res.Entity, fieldNames),
	}}}, nil
}

// VisibleIndexes describes the entity's indexes a caller seeing visible can
// filter by: each with its leading key parts that are visible, unmasked
// columns (a column prefix counts as its column); an index whose first part
// is not one is left out. Unique only when every part is listed; a partial
// index says so without its predicate, which may name what the caller does
// not see; expressions are never shown.
func VisibleIndexes(e entity.Entity, visible []string) []map[string]any {
	out := []map[string]any{}
	for _, ix := range e.Indexes {
		var fields []string
		for _, part := range ix.Parts {
			column := indexColumn(part)
			a, ok := e.AttributeByName(column)
			if !ok || a.Name != column || a.Excluded || a.Mask != "" || !slices.Contains(visible, column) {
				break
			}
			fields = append(fields, column)
		}
		if len(fields) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"name": ix.Name, "fields": fields, "method": ix.Method,
			"unique": ix.Unique && len(fields) == len(ix.Parts), "partial": ix.Where != "",
		})
	}
	return out
}

// indexColumn is the column an index part names: itself, or the column of a
// prefix part such as title(20); an expression names none that matches.
func indexColumn(part string) string {
	if i := strings.IndexByte(part, '('); i > 0 && strings.HasSuffix(part, ")") {
		if _, err := strconv.Atoi(part[i+1 : len(part)-1]); err == nil {
			return part[:i]
		}
	}
	return part
}

// indexHint names the indexed fields a refused statement could filter on.
func indexHint(e entity.Entity, visible []string) string {
	var names []string
	for _, ix := range VisibleIndexes(e, visible) {
		f := ix["fields"].([]string)
		if len(f) == 1 {
			names = append(names, f[0])
		} else {
			names = append(names, "("+strings.Join(f, ", ")+")")
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "indexed fields to filter on (an index serves its leading fields): " + strings.Join(slices.Compact(names), ", ")
}

// withIndexHints adds to a cost refusal of a statement on e the indexed
// fields its caller could filter on.
func withIndexHints(ctx context.Context, tc Context, e entity.Entity, err error) error {
	var exceeded *cost.ExceededError
	if !errors.As(err, &exceeded) || len(e.Indexes) == 0 {
		return err
	}
	if hint := indexHint(e, readableFields(ctx, tc, e)); hint != "" {
		exceeded.Hints = append([]string{hint}, exceeded.Hints...)
	}
	return err
}

// VisibleKeys lists the entity's identity keys whose every column is among
// visible, the fields a caller can use to address one row.
func VisibleKeys(e entity.Entity, visible []string) [][]string {
	out := [][]string{}
	for _, key := range e.IdentityKeys(false, false) {
		if !slices.ContainsFunc(key, func(c string) bool { return !slices.Contains(visible, c) }) {
			out = append(out, key)
		}
	}
	return out
}

// readableFields are the fields of e the caller reads or aggregates, as
// describeFields finds them, asked of the authorizer alone: a hint for a
// refused call is no authorization of its own to trace or audit.
func readableFields(ctx context.Context, tc Context, e entity.Entity) []string {
	fields := map[string]bool{}
	for _, action := range []entity.Action{entity.ActionRead, entity.ActionAggregate} {
		if tc.Authorizer == nil {
			for _, a := range e.Attributes {
				fields[a.Name] = !a.Excluded
			}
			break
		}
		dec, err := tc.Authorizer.Authorize(ctx, rbac.Request{Role: tc.Role, Subject: tc.Subject, Entity: e.Name,
			Action: action})
		if err != nil || !dec.Reachable() {
			continue
		}
		for _, scope := range append(dec.Scopes(), dec.Fields) {
			for _, f := range scope {
				if a, ok := e.AttributeByName(f); ok {
					fields[a.Name] = true
				}
			}
		}
	}
	return e.OrderedNames(fields)
}
