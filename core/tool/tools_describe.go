package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

// ---- describe_entities ----

// DescribeTool lists entities or one entity's fields.
type DescribeTool struct{}

// Info implements Tool.
func (DescribeTool) Info() Info {
	return Info{
		Name:        "describe_entities",
		Action:      "describe",
		Description: "List exposed entities and their fields",
		InputSchema: schemaDescribe,
		ReadOnly:    true,
	}
}

// Enabled implements Tool.
func (DescribeTool) Enabled(f config.ToolFlags) bool { return f.DescribeEntities }

// Run implements Tool.
func (DescribeTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in struct {
		Entity string `json:"entity"`
	}
	if len(input) > 0 {
		if err := decodeInput(input, &in); err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
	}
	if in.Entity != "" {
		return describeEntity(ctx, tc, in.Entity)
	}
	entities := tc.Registry.Entities()
	out := make([]map[string]any, 0, len(entities))
	for _, e := range entities {
		if !e.MCP.DMLTools {
			continue
		}
		_, allowed, err := describeFields(ctx, tc, e)
		if err != nil {
			return Result{}, err
		}
		if !allowed {
			continue
		}
		out = append(out, map[string]any{"name": e.Name, "description": e.Description})
	}
	return Result{Content: out}, nil
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
		})
	}
	return Result{Content: []map[string]any{{
		"name": res.Entity.Name, "description": res.Entity.Description, "fields": fields,
	}}}, nil
}
