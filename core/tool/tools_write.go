package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// ---- create_record ----

// CreateTool inserts one record.
type CreateTool struct{}

func (CreateTool) Info() Info {
	return Info{
		Name: "create_record", Title: "Create record", InputSchema: schemaCreate,
		Description: "Insert one row into an entity, setting only fields you may write. Returns the new " +
			"row's key and rowsAffected. Pass a transaction token to run it inside begin_transaction.",
		Action: entity.ActionCreate.String(), Object: true,
	}
}
func (CreateTool) Enabled(f config.ToolFlags) bool { return f.CreateRecord }
func (CreateTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	in, err := decodeTool(input, func(in *createInput) error { return normalizeMapValues(in.Values) })
	if err != nil {
		return Result{}, err
	}
	if len(in.Values) == 0 {
		return Result{}, fmt.Errorf("%w: values must set at least one field", ErrInvalidInput)
	}
	return runInsert(ctx, tc, in)
}

// ---- update_record ----

// UpdateTool updates records (cost-gated).
type UpdateTool struct{}

func (UpdateTool) Info() Info {
	return Info{
		Name: "update_record", Title: "Update records", InputSchema: schemaUpdate,
		Description: "Set fields on the rows of an entity matching a filter (required; narrow it to the " +
			"rows you mean, ideally by key). Returns rowsAffected. A write with no filter is refused with " +
			"UNSAFE_WRITE, and one estimated to touch too many rows with COST_EXCEEDED.",
		Action: entity.ActionUpdate.String(), Destructive: true, Idempotent: true, Object: true,
	}
}
func (UpdateTool) Enabled(f config.ToolFlags) bool { return f.UpdateRecord }
func (UpdateTool) CostGated()                      {}
func (UpdateTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	in, err := decodeTool(input, func(in *updateInput) error { return normalizeMapValues(in.Set) })
	if err != nil {
		return Result{}, err
	}
	return runFilteredWrite(ctx, tc, in.Transaction, func(ctx context.Context, tc Context) (statement, error) {
		return prepareUpdate(ctx, tc, in)
	})
}

// ---- delete_record ----

// DeleteTool deletes records (cost-gated, off by default).
type DeleteTool struct{}

func (DeleteTool) Info() Info {
	return Info{
		Name: "delete_record", Title: "Delete records", InputSchema: schemaDelete,
		Description: "Delete the rows of an entity matching a filter (required; narrow it to the rows " +
			"you mean, ideally by key). Returns rowsAffected. A delete with no filter is refused with " +
			"UNSAFE_WRITE, and one estimated to touch too many rows with COST_EXCEEDED.",
		Action: entity.ActionDelete.String(), Destructive: true, Idempotent: true, Object: true,
	}
}
func (DeleteTool) Enabled(f config.ToolFlags) bool { return f.DeleteRecord }
func (DeleteTool) CostGated()                      {}
func (DeleteTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	in, err := decodeTool[deleteInput](input, nil)
	if err != nil {
		return Result{}, err
	}
	return runFilteredWrite(ctx, tc, in.Transaction, func(ctx context.Context, tc Context) (statement, error) {
		return prepareDelete(ctx, tc, in)
	})
}
