package entity

import (
	"github.com/nethinwei/sql-mcp-server/relalg"
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

// ConstraintKind names a domain constraint.
type ConstraintKind uint8

const (
	// ConstraintNotNull forbids nulls.
	ConstraintNotNull ConstraintKind = iota
	// ConstraintUnique requires uniqueness.
	ConstraintUnique
	// ConstraintCheck is an arbitrary check (Expr is informational only).
	ConstraintCheck
	// ConstraintRange bounds a numeric value (Expr is informational only).
	ConstraintRange
)

// Constraint is a domain constraint.
type Constraint struct {
	Kind ConstraintKind
	Expr string
}

// Domain is a value domain: a SQL type plus constraints. The type is
// informational; IR construction validates value kinds against it where it can.
type Domain struct {
	Type        string
	Nullable    bool
	Constraints []Constraint
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
	Name        string
	Columns     []string
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
	Relations   []Relationship
	// Params is the ordered list of formal parameter names for a KindProcedure
	// entity. execute_entity binds a caller's named args to positional CALL
	// placeholders in this exact order; a stored procedure whose params are not
	// declared cannot be executed (fail-closed) since positional binding would
	// otherwise be guesswork.
	Params []string
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

// Resolved is a request-time view of an entity with field projection applied
// (excluded attributes removed). RBAC further restricts Attributes per role.
type Resolved struct {
	Entity     Entity
	Attributes []Attribute
}
