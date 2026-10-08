package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// ---- create_record ----

// CreateTool inserts one record.
type CreateTool struct{}

func (CreateTool) Info() Info {
	return Info{
		Name: "create_record", Description: "Create a record in an entity", InputSchema: schemaCreate,
		Action: entity.ActionCreate.String(),
	}
}
func (CreateTool) Enabled(f config.ToolFlags) bool { return f.CreateRecord }
func (CreateTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in createInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if err := normalizeMapValues(in.Values); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runInsert(ctx, tc, in)
}

// ---- update_record ----

// UpdateTool updates records (cost-gated).
type UpdateTool struct{}

func (UpdateTool) Info() Info {
	return Info{
		Name: "update_record", Description: "Update records in an entity", InputSchema: schemaUpdate,
		Action: entity.ActionUpdate.String(),
	}
}
func (UpdateTool) Enabled(f config.ToolFlags) bool { return f.UpdateRecord }
func (UpdateTool) CostGated()                      {}
func (UpdateTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in updateInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if err := normalizeMapValues(in.Set); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runFilteredWrite(ctx, tc, in.Transaction, func(ctx context.Context, tc Context) (
		writePlan, codegen.Compiled, error,
	) {
		return prepareUpdate(ctx, tc, in)
	})
}

// ---- delete_record ----

// DeleteTool deletes records (cost-gated, off by default).
type DeleteTool struct{}

func (DeleteTool) Info() Info {
	return Info{
		Name: "delete_record", Description: "Delete records from an entity", InputSchema: schemaDelete,
		Action: entity.ActionDelete.String(),
	}
}
func (DeleteTool) Enabled(f config.ToolFlags) bool { return f.DeleteRecord }
func (DeleteTool) CostGated()                      {}
func (DeleteTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in deleteInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runFilteredWrite(ctx, tc, in.Transaction, func(ctx context.Context, tc Context) (
		writePlan, codegen.Compiled, error,
	) {
		return prepareDelete(ctx, tc, in)
	})
}
