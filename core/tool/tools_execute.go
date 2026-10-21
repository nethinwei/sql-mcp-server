package tool

import (
	"cmp"
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
	return Info{
		Name: "execute_entity", Title: "Execute stored procedure", InputSchema: schemaExecute,
		Description: "Call a stored procedure entity with its parameters as args, returned as " +
			"{\"result\": [rows]}. A procedure may change data.",
		Action: entity.ActionExecute.String(), Destructive: true,
	}
}
func (ExecuteTool) Enabled(f config.ToolFlags) bool { return f.ExecuteEntity }
func (ExecuteTool) CostGated()                      {}
func (ExecuteTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	in, err := decodeTool(input, func(in *executeInput) error { return normalizeMapValues(in.Args) })
	if err != nil {
		return Result{}, err
	}
	return runExecute(ctx, tc, in, true)
}

// ProcedureTool exposes one configured procedure as an independent MCP tool.
// It accepts the procedure parameters directly and delegates execution to the
// same implementation as execute_entity.
type ProcedureTool struct {
	Entity entity.Entity
	// Name is the tool's name among the exposed procedures (see
	// ProcedureTools); procedureToolBase when empty.
	Name string
}

// procedureToolBase is a procedure's readable MCP-safe tool name: procedure_
// and its local name, with characters a tool name may not hold replaced. The
// prefix separates procedure tools from the built-in ones.
func procedureToolBase(e entity.Entity) string {
	local := e.Local
	if local == "" {
		local = e.Name
	}
	var normalized strings.Builder
	for _, r := range local {
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
	return "procedure_" + base[:min(len(base), 100)]
}

// ProcedureTools returns the procedures exposed as their own tools: trusted
// procedures configured with a custom tool. Each is named procedureToolBase;
// procedures whose base names collide (same-named procedures of different
// namespaces, or names that normalize alike) add a hash of their Name.
func ProcedureTools(reg *entity.Registry) []ProcedureTool {
	var out []ProcedureTool
	bases := map[string]int{}
	for e := range reg.Procedures() {
		if e.MCP.CustomTool && e.MCP.TrustedProcedure {
			t := ProcedureTool{Entity: e, Name: procedureToolBase(e)}
			bases[t.Name]++
			out = append(out, t)
		}
	}
	for i, t := range out {
		if bases[t.Name] > 1 {
			sum := sha256.Sum256([]byte(t.Entity.Name))
			out[i].Name += "_" + hex.EncodeToString(sum[:4])
		}
	}
	return out
}

// FindProcedureTool returns the exposed procedure tool named name.
func FindProcedureTool(reg *entity.Registry, name string) (ProcedureTool, bool) {
	if !strings.HasPrefix(name, "procedure_") {
		return ProcedureTool{}, false
	}
	for _, t := range ProcedureTools(reg) {
		if t.Name == name {
			return t, true
		}
	}
	return ProcedureTool{}, false
}

// Info implements Tool.
func (t ProcedureTool) Info() Info {
	properties := make(map[string]any, len(t.Entity.Params))
	for _, param := range t.Entity.Params {
		properties[param] = map[string]any{"description": "Stored procedure parameter"}
	}
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             append([]string{}, t.Entity.Params...), // never null
		"additionalProperties": false,
	})
	return Info{
		Name:   cmp.Or(t.Name, procedureToolBase(t.Entity)),
		Action: entity.ActionExecute.String(),
		Title:  "Execute " + t.Entity.Name,
		Description: cmp.Or(t.Entity.Description, "Execute stored procedure "+t.Entity.Name) +
			"; the procedure may change data. Returns {\"result\": [rows]}.",
		InputSchema: schema,
		Destructive: true,
	}
}

// Enabled implements Tool. Procedure tools are registered independently of the
// generic tool flags.
func (ProcedureTool) Enabled(config.ToolFlags) bool { return true }
func (ProcedureTool) CostGated()                    {}

// Run implements Tool.
func (t ProcedureTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	args, err := decodeTool(input, func(args *map[string]any) error { return normalizeMapValues(*args) })
	if err != nil {
		return Result{}, err
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
