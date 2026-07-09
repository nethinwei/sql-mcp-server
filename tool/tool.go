package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nethinwei/sql-mcp-server/cache"
	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/dialect"
	"github.com/nethinwei/sql-mcp-server/engine"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/hook"
	"github.com/nethinwei/sql-mcp-server/mask"
	"github.com/nethinwei/sql-mcp-server/rbac"
	"github.com/nethinwei/sql-mcp-server/relalg"
	"github.com/nethinwei/sql-mcp-server/store"
)

// Sentinel errors. Business errors (Unauthorized/NotFound/UnsafeWrite/Cost)
// are mapped by x/mcpserver to IsError results; protocol errors to JSON-RPC.
var (
	// ErrEntityNotFound is returned when the requested entity is unknown.
	ErrEntityNotFound = errors.New("tool: entity not found")
	// ErrUnauthorized is returned when the role lacks the action.
	ErrUnauthorized = errors.New("tool: unauthorized")
	// ErrInvalidInput is returned for malformed tool input.
	ErrInvalidInput = errors.New("tool: invalid input")
	// ErrUnsafeWrite is returned when an update/delete lacks a PK-scoped filter.
	ErrUnsafeWrite = errors.New("tool: unsafe write without primary-key filter")
	// ErrNotImplemented is returned by stub tools.
	ErrNotImplemented = errors.New("tool: not implemented")
	// ErrDuplicateTool is returned by NewRegistry for duplicate tool names.
	ErrDuplicateTool = errors.New("tool: duplicate name")
)

// Info is a tool's static metadata, mapped to an MCP tool definition.
type Info struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	ReadOnly    bool
}

// Result is a tool's outcome. Content holds structured rows/text; IsError marks
// a business-level error the agent can act on.
type Result struct {
	Content          []map[string]any
	IsError          bool
	StructuredResult any
}

// Context carries per-request dependencies (injected, no mutable global state).
type Context struct {
	Role       string
	DB         store.DB
	Dialect    dialect.Dialect
	Registry   *entity.Registry
	Authorizer rbac.Authorizer
	Masker     mask.Masker
	Gate       cost.Gate
	Cache      cache.Cache[[]map[string]any]
	Engine     *engine.Engine
	Hooks      *hook.Hooks
	Timeout    time.Duration
}

// Tool is a single DML capability.
type Tool interface {
	Info() Info
	Enabled(flags config.ToolFlags) bool
	Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error)
}

// CostGated marks a tool whose execution must pass the cost gate.
type CostGated interface {
	Tool
	CostGated()
}

// Registry holds an immutable set of tools.
type Registry struct {
	tools  []Tool
	byName map[string]Tool
}

// NewRegistry builds a Registry, rejecting duplicate tool names.
func NewRegistry(tools []Tool) (*Registry, error) {
	r := &Registry{byName: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		name := t.Info().Name
		if _, ok := r.byName[name]; ok {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateTool, name)
		}
		r.byName[name] = t
		r.tools = append(r.tools, t)
	}
	return r, nil
}

// Tools returns all registered tools.
func (r *Registry) Tools() []Tool {
	out := make([]Tool, len(r.tools))
	copy(out, r.tools)
	return out
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// Enabled returns the tools whose Enabled(flags) is true.
func (r *Registry) Enabled(flags config.ToolFlags) []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if t.Enabled(flags) {
			out = append(out, t)
		}
	}
	return out
}

// ---- input shapes ----

type condJSON struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

type aggJSON struct {
	Func  string `json:"func"`
	Field string `json:"field"`
}

type readInput struct {
	Entity string     `json:"entity"`
	Fields []string   `json:"fields,omitempty"`
	Filter []condJSON `json:"filter,omitempty"`
	Limit  int64      `json:"limit,omitempty"`
}

type createInput struct {
	Entity string         `json:"entity"`
	Values map[string]any `json:"values"`
}

type updateInput struct {
	Entity string         `json:"entity"`
	Filter []condJSON     `json:"filter"`
	Set    map[string]any `json:"set"`
}

type deleteInput struct {
	Entity string     `json:"entity"`
	Filter []condJSON `json:"filter"`
}

type aggregateInput struct {
	Entity     string     `json:"entity"`
	GroupBy    []string   `json:"groupBy,omitempty"`
	Aggregates []aggJSON  `json:"aggregates"`
	Filter     []condJSON `json:"filter,omitempty"`
}

// ---- helpers ----

func filterToPredicate(conds []condJSON) (relalg.Predicate, error) {
	if len(conds) == 0 {
		return nil, nil
	}
	var preds []relalg.Predicate
	for _, c := range conds {
		op := relalg.Op(c.Op)
		if !op.Valid() {
			return nil, fmt.Errorf("%w: operator %q", ErrInvalidInput, c.Op)
		}
		preds = append(preds, relalg.Condition{Field: c.Field, Op: op, Value: c.Value})
	}
	if len(preds) == 1 {
		return preds[0], nil
	}
	return relalg.And{Preds: preds}, nil
}

func andPreds(a, b relalg.Predicate) relalg.Predicate {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return relalg.And{Preds: []relalg.Predicate{a, b}}
}

func toExceededError(d cost.Decision) error {
	p := cost.Plan{}
	if d.Plan != nil {
		p = *d.Plan
	}
	s := cost.Score{}
	if d.Score != nil {
		s = *d.Score
	}
	return &cost.ExceededError{Plan: p, Score: s, Hints: d.Hints, Soft: d.Soft}
}

func maskRow(m mask.Masker, attrs []entity.Attribute, row map[string]any) {
	rules := make(map[string]string, len(attrs))
	for _, a := range attrs {
		if a.Mask != "" {
			rules[a.Name] = a.Mask
		}
	}
	for f, v := range row {
		if rule, ok := rules[f]; ok {
			if mv, err := m.Mask(rule, v); err == nil {
				row[f] = mv
			}
		}
	}
}

func argsKey(args []any) string {
	return fmt.Sprint(args)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// withTimeout returns a context bounded by tc.Timeout, or ctx unchanged when
// no timeout is configured. The cancel func is always non-nil.
func withTimeout(ctx context.Context, tc Context) (context.Context, context.CancelFunc) {
	if tc.Timeout > 0 {
		return context.WithTimeout(ctx, tc.Timeout)
	}
	return ctx, func() {}
}
