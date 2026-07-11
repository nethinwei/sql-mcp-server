package tool

import (
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

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
