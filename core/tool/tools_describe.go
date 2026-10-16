package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
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
	for _, e := range tc.Registry.Entities() {
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
		"keys": VisibleKeys(res.Entity, fieldNames),
	}}}, nil
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
