// Package configplan turns a plan, a scope of scanned tables and rules to
// apply to them, into a proposal: the configuration changes and a report of
// what they cover, what changes, the exceptions to look at, and when the
// changes take effect. It is deterministic and reads no database: the
// console, saved templates and the configuration MCP endpoint all evaluate
// plans here, against the scanned catalog and a base configuration.
package configplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

// Plan selects tables and says what to do with them. Its JSON form is what
// templates store and the MCP tools take.
type Plan struct {
	Scope Scope `json:"scope"`
	// Import adds an entity for each table in scope no entity reads.
	Import bool `json:"import,omitempty"`
	// Grants grant actions on the entities of the tables in scope.
	Grants []GrantRule `json:"grants,omitempty"`
	// Fields hide or mask the fields of those entities by column.
	Fields []FieldRule `json:"fields,omitempty"`
	// Sync brings their fields in line with the scanned columns.
	Sync *SyncRule `json:"sync,omitempty"`
	// Exclude leaves tables out of the plan, by Ref.
	Exclude []string `json:"exclude,omitempty"`
	// Names names the entities imported for tables, by Ref.
	Names map[string]string `json:"names,omitempty"`
}

// Scope selects scanned tables; every condition set must hold.
type Scope struct {
	// Datasources limits the scope to these datasources; empty: all.
	Datasources []string `json:"datasources,omitempty"`
	// Schemas are glob patterns (* and ?) on schema names; empty: all.
	Schemas []string `json:"schemas,omitempty"`
	// Tables are glob patterns on table names; empty: all.
	Tables []string `json:"tables,omitempty"`
	// Search matches a part of the table name or its comment.
	Search string `json:"search,omitempty"`
	// Status limits the scope to tables in these states; empty: all.
	Status []Status `json:"status,omitempty"`
	// Drift limits the scope to configured tables whose columns and fields
	// differ.
	Drift bool `json:"drift,omitempty"`
	// Refs limits the scope to these tables.
	Refs []string `json:"refs,omitempty"`
	// Entities limits the scope to the tables of these configured entities,
	// by ID.
	Entities []string `json:"entities,omitempty"`
}

// Status is where a table stands against the configuration.
type Status string

// Table states.
const (
	// StatusNew tables are read by no entity.
	StatusNew Status = "new"
	// StatusWorkspace tables are read by an entity not yet published.
	StatusWorkspace Status = "workspace"
	// StatusPublished tables are read by a published entity.
	StatusPublished Status = "published"
)

// GrantRule grants actions to a role or a user.
type GrantRule struct {
	// Principal is role:<name> or user:<name>; a role is created if needed.
	Principal string   `json:"principal"`
	Actions   []string `json:"actions"`
	// Fields are glob patterns of the fields granted; empty: every field.
	Fields []string `json:"fields,omitempty"`
	// Rows filters the rows granted, as in a grant.
	Rows config.FilterConfig `json:"rows,omitempty"`
}

// FieldRule hides or masks the fields of matching columns.
type FieldRule struct {
	// Columns are glob patterns on column names.
	Columns []string `json:"columns"`
	// Types are glob patterns on column types; empty: any type.
	Types   []string `json:"types,omitempty"`
	Exclude bool     `json:"exclude,omitempty"`
	Mask    string   `json:"mask,omitempty"`
}

// SyncRule adds fields for new columns and removes those of dropped ones,
// brings keys and kinds in line, and removes the entities of dropped tables.
type SyncRule struct {
	Add    bool `json:"add,omitempty"`
	Remove bool `json:"remove,omitempty"`
	// HideNew excludes the fields it adds until they are reviewed.
	HideNew bool `json:"hideNew,omitempty"`
	// RemoveTables removes the entities whose tables are gone (see
	// GoneTables), with the grants, relationships and affects naming them.
	RemoveTables bool `json:"removeTables,omitempty"`
	// Keys sets the primary and unique keys of tables to those scanned, and
	// the kind (table or view) of each entity to its table's. Relationships
	// are reported where they and the foreign keys differ, not changed: a
	// configured relationship is not known to come from a foreign key.
	Keys bool `json:"keys,omitempty"`
}

var grantable = []string{"read", "aggregate", "create", "update", "delete"}

// Decode reads a plan, rejecting unknown keys, and checks it.
func Decode(data []byte) (Plan, error) {
	var p Plan
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Plan{}, fmt.Errorf("plan: %w", err)
	}
	return p, p.Check()
}

// Check reports a plan that cannot be evaluated.
func (p *Plan) Check() error {
	var errs []error
	for _, pattern := range slices.Concat(p.Scope.Schemas, p.Scope.Tables) {
		errs = append(errs, checkGlob(pattern))
	}
	for _, s := range p.Scope.Status {
		if !slices.Contains([]Status{StatusNew, StatusWorkspace, StatusPublished}, s) {
			errs = append(errs, fmt.Errorf("plan: unknown status %q", s))
		}
	}
	for i := range p.Grants {
		errs = append(errs, p.Grants[i].check())
	}
	for _, f := range p.Fields {
		if len(f.Columns) == 0 || !f.Exclude && f.Mask == "" {
			errs = append(errs, errors.New("plan: a field rule needs columns and exclude or mask"))
		}
		for _, pattern := range slices.Concat(f.Columns, f.Types) {
			errs = append(errs, checkGlob(pattern))
		}
	}
	syncs := p.Sync != nil && (p.Sync.Add || p.Sync.Remove || p.Sync.RemoveTables || p.Sync.Keys)
	if !p.Import && len(p.Grants) == 0 && len(p.Fields) == 0 && !syncs {
		errs = append(errs, errors.New("plan: nothing to do: set import, grants, fields or sync"))
	}
	return errors.Join(errs...)
}

func (g *GrantRule) check() error {
	kind, name, ok := strings.Cut(g.Principal, ":")
	if !ok || name == "" || kind != "role" && kind != "user" {
		return fmt.Errorf("plan: principal %q must be role:<name> or user:<name>", g.Principal)
	}
	if len(g.Actions) == 0 {
		return fmt.Errorf("plan: grant to %s needs actions", g.Principal)
	}
	for i, a := range g.Actions {
		g.Actions[i] = strings.ToLower(a)
		if !slices.Contains(grantable, g.Actions[i]) {
			return fmt.Errorf("plan: unknown action %q (%s)", a, strings.Join(grantable, ", "))
		}
	}
	slices.SortFunc(g.Actions, func(a, b string) int { return slices.Index(grantable, a) - slices.Index(grantable, b) })
	g.Actions = slices.Compact(g.Actions)
	for _, pattern := range g.Fields {
		if err := checkGlob(pattern); err != nil {
			return err
		}
	}
	return nil
}

func checkGlob(pattern string) error {
	if _, err := path.Match(pattern, ""); err != nil || pattern == "" {
		return fmt.Errorf("plan: invalid pattern %q", pattern)
	}
	return nil
}

// globs are glob patterns compiled for matching many names, ignoring case:
// a pattern without wildcards is a name to compare.
type globs struct {
	names    map[string]bool
	patterns []string
}

func compileGlobs(patterns []string) globs {
	if len(patterns) == 0 {
		return globs{}
	}
	g := globs{names: map[string]bool{}}
	for _, p := range patterns {
		p = strings.ToLower(p)
		if strings.ContainsAny(p, "*?[\\") {
			g.patterns = append(g.patterns, p)
		} else {
			g.names[p] = true
		}
	}
	return g
}

// set reports whether there are patterns.
func (g globs) set() bool { return g.names != nil }

// match reports whether s matches one of the patterns.
func (g globs) match(s string) bool {
	s = strings.ToLower(s)
	if g.names[s] {
		return true
	}
	return slices.ContainsFunc(g.patterns, func(p string) bool {
		ok, _ := path.Match(p, s)
		return ok
	})
}
