package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// ---- execute_entity ----

// ExecuteTool calls a stored procedure.
type ExecuteTool struct{}

func (ExecuteTool) Info() Info {
	return Info{Name: "execute_entity", Description: "Execute a stored procedure", InputSchema: schemaExecute}
}
func (ExecuteTool) Enabled(f config.ToolFlags) bool { return f.ExecuteEntity }
func (ExecuteTool) CostGated()                      {}
func (ExecuteTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in executeInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if err := normalizeMapValues(in.Args); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runExecute(ctx, tc, in, true)
}

// ProcedureTool exposes one configured procedure as an independent MCP tool.
// It accepts the procedure parameters directly and delegates execution to the
// same implementation as execute_entity.
type ProcedureTool struct {
	Entity entity.Entity
}

// ProcedureToolName returns a stable MCP-safe name. The prefix separates
// procedure tools from built-ins; the hash prevents normalization collisions.
func ProcedureToolName(entityName string) string {
	var normalized strings.Builder
	for _, r := range entityName {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			normalized.WriteRune(r)
		default:
			normalized.WriteByte('_')
		}
	}
	base := normalized.String()
	if base == "" {
		base = "procedure"
	}
	sum := sha256.Sum256([]byte(entityName))
	return "procedure_" + base + "_" + hex.EncodeToString(sum[:4])
}

// Info implements Tool.
func (t ProcedureTool) Info() Info {
	properties := make(map[string]any, len(t.Entity.Params))
	for _, param := range t.Entity.Params {
		properties[param] = map[string]any{"description": "Stored procedure parameter"}
	}
	schema, _ := json.Marshal(map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   t.Entity.Params,
	})
	return Info{
		Name:        ProcedureToolName(t.Entity.Name),
		Description: "Execute stored procedure " + t.Entity.Name,
		InputSchema: schema,
	}
}

// Enabled implements Tool. Procedure tools are registered independently of the
// generic tool flags.
func (ProcedureTool) Enabled(config.ToolFlags) bool { return true }
func (ProcedureTool) CostGated()                    {}

// Run implements Tool.
func (t ProcedureTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var args map[string]any
	if err := decodeInput(input, &args); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if err := normalizeMapValues(args); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runExecute(ctx, tc, executeInput{Entity: t.Entity.Name, Args: args}, false)
}

// orderedProcArgs binds named args to the procedure's declared parameter order.
// It rejects an undeclared procedure, unknown args, and missing args so a
// positional CALL never receives values in the wrong slots.
func orderedProcArgs(params []string, in map[string]any) ([]any, error) {
	if len(params) == 0 {
		if len(in) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: procedure declares no parameters", ErrInvalidInput)
	}
	want := make(map[string]bool, len(params))
	for _, p := range params {
		want[p] = true
	}
	for k := range in {
		if !want[k] {
			return nil, fmt.Errorf("%w: unknown procedure parameter %q", ErrInvalidInput, k)
		}
	}
	args := make([]any, len(params))
	for i, p := range params {
		v, ok := in[p]
		if !ok {
			return nil, fmt.Errorf("%w: missing procedure parameter %q", ErrInvalidInput, p)
		}
		args[i] = v
	}
	return args, nil
}
