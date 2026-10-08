package entity

import (
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// Kind classifies a database object backing an entity.
type Kind uint8

const (
	// KindTable is a base table.
	KindTable Kind = iota
	// KindView is a view.
	KindView
	// KindProcedure is a stored procedure.
	KindProcedure
)

// Action names a DML operation for role permissions.
type Action uint8

const (
	// ActionRead selects records.
	ActionRead Action = iota
	// ActionCreate inserts records.
	ActionCreate
	// ActionUpdate updates records.
	ActionUpdate
	// ActionDelete deletes records.
	ActionDelete
	// ActionExecute calls a stored procedure.
	ActionExecute
	// ActionAggregate groups/aggregate-queries records.
	ActionAggregate
)

// String returns the action name.
func (a Action) String() string {
	switch a {
	case ActionRead:
		return "read"
	case ActionCreate:
		return "create"
	case ActionUpdate:
		return "update"
	case ActionDelete:
		return "delete"
	case ActionExecute:
		return "execute"
	case ActionAggregate:
		return "aggregate"
	}
	return "unknown"
}

// Domain is a column's value domain as introspected from the database. It is
// informational: shown to agents and the console, not enforced.
type Domain struct {
	Type     string
	Nullable bool
}

// Attribute is one column of a relation, with projection and masking controls.
type Attribute struct {
	Name        string
	Alias       string
	Description string
	Domain      Domain
	Excluded    bool   // true removes the column from all responses (field projection)
	Mask        string // optional mask rule name (see mask package)
}

// Key is a candidate key; Primary marks the primary key.
type Key struct {
	Name    string
	Columns []string
	Primary bool
}

// ForeignKey declares referential integrity to another relation.
type ForeignKey struct {
	Name    string
	Columns []string
	// RefSchema is the referenced relation's schema; it may differ from the
	// referencing table's.
	RefSchema   string
	RefRelation string
	RefColumns  []string
}

// RoleAccess maps each action to the roles allowed to perform it.
type RoleAccess map[Action][]string

// FieldPermissions is one role's field-level access. Read applies to
// projections, filters, grouping, and aggregate inputs; Write applies to
// create values and update assignments.
type FieldPermissions struct {
	Read  []string
	Write []string
}

// FieldAccess maps role names to field-level permissions. Absence of a role
// preserves entity-level authorization behavior for backwards compatibility.
type FieldAccess map[string]FieldPermissions

// MCPFlags controls how an entity participates in MCP.
type MCPFlags struct {
	DMLTools         bool // expose the seven DML tools for this entity
	CustomTool       bool // register a stored procedure as a named tool
	TrustedProcedure bool // procedure passed explicit DBA cost/safety review
}

// RowPolicies maps a role name to a row-level filter predicate. The rbac
// package ANDs the role's predicate with the request predicate.
type RowPolicies map[string]relalg.Predicate

// Relationship describes a link to another entity for nested expansion.
type Relationship struct {
	Name        string
	Target      string
	Cardinality string
	JoinOn      map[string]string // this field -> target field
}

// Entity is a complete description of an exposed relation.
type Entity struct {
	Name        string
	Source      string
	DataSource  string
	Schema      string
	Description string
	Kind        Kind
	Attributes  []Attribute
	Keys        []Key
	ForeignKeys []ForeignKey
	Role        RoleAccess
	FieldAccess FieldAccess
	MCP         MCPFlags
	RowPolicies RowPolicies
	// TenantPolicy is ANDed for every principal and never merged across
	// grants; nil means the entity has no tenant boundary.
	TenantPolicy relalg.Predicate
	Relations    []Relationship
	// Params is the ordered list of formal parameter names for a KindProcedure
	// entity. execute_entity binds a caller's named args to positional CALL
	// placeholders in this exact order; a stored procedure whose params are not
	// declared cannot be executed (fail-closed) since positional binding would
	// otherwise be guesswork.
	Params []string
}

// ActionsFor lists the actions applicable to an entity kind: execute for a
// procedure, otherwise the record actions.
func ActionsFor(kind Kind) []Action {
	if kind == KindProcedure {
		return []Action{ActionExecute}
	}
	return []Action{ActionRead, ActionAggregate, ActionCreate, ActionUpdate, ActionDelete}
}

// DatasourceName is the entity's datasource, "default" when unset.
func (e Entity) DatasourceName() string {
	if e.DataSource == "" {
		return "default"
	}
	return e.DataSource
}

// PrimaryKey returns the columns of the primary key, or nil if none is declared.
func (e Entity) PrimaryKey() []string {
	for _, k := range e.Keys {
		if k.Primary {
			return k.Columns
		}
	}
	return nil
}

// AttributeByName returns the attribute with the given logical name and a
// found flag. It matches Name or Alias.
func (e Entity) AttributeByName(name string) (Attribute, bool) {
	for _, a := range e.Attributes {
		if a.Name == name || a.Alias == name {
			return a, true
		}
	}
	return Attribute{}, false
}

// OrderedNames returns the attribute names in set, in attribute order.
func (e Entity) OrderedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for _, a := range e.Attributes {
		if set[a.Name] {
			out = append(out, a.Name)
		}
	}
	return out
}

// Resolved is a request-time view of an entity with field projection applied
// (excluded attributes removed). RBAC further restricts Attributes per role.
type Resolved struct {
	Entity     Entity
	Attributes []Attribute
}
