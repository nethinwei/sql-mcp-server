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

// Domain is a column's value domain as introspected from the database.
type Domain struct {
	Type     string
	Nullable bool
	// Default reports that the database supplies a value when an insert
	// omits the column (a default expression, a sequence, an identity BY
	// DEFAULT or auto_increment).
	Default bool
	// Generated reports that the database computes the value (a generated
	// column or an identity ALWAYS): the column cannot be written.
	Generated bool
	// AutoIncrement reports a value taken from a sequence on insert.
	AutoIncrement bool
}

// Writable reports whether creates and updates may set the column.
func (d Domain) Writable() bool { return !d.Generated }

// Required reports whether a create must supply the column.
func (d Domain) Required() bool { return !d.Nullable && !d.Default && !d.Generated }

// Attribute is one column of a relation, with projection and masking controls.
type Attribute struct {
	Name        string
	Alias       string
	Description string
	Domain      Domain
	Excluded    bool   // true removes the column from all responses (field projection)
	Mask        string // optional mask rule name (see mask package)
}

// Reasons a unique key does not identify a row.
const (
	KeyPartial    = "partial"    // a partial unique index (WHERE ...)
	KeyExpression = "expression" // an index on expressions, such as lower(email)
	KeyPrefix     = "prefix"     // a MySQL prefix index, such as email(10)
	KeyNullable   = "nullable"   // a nullable column: several rows may hold NULL
)

// Key is a primary or unique key. A unique key that cannot identify a row
// is kept for display, with Reason saying why.
type Key struct {
	Name    string
	Columns []string
	Primary bool
	// Declared marks a key the database does not guarantee, configured on a
	// view or foreign table: it serves reads, never the write-safety check.
	Declared bool
	// Deferrable reports a constraint checked at commit: inside a
	// transaction it may be violated, so it identifies no row there.
	Deferrable bool
	// Reason is empty for a key that identifies at most one row.
	Reason string
}

// Index is an index of a table in the database's own terms: databases
// support different structures and forms of index.
type Index struct {
	Name string
	// Method is the database's name for the index structure, lower-cased:
	// for example btree, hash, gin, gist, brin or spgist on PostgreSQL, and
	// btree, hash, fulltext or spatial on MySQL and OceanBase.
	Method string
	// Parts are the key parts in order: a column, a column prefix such as
	// title(20), or an expression as the database prints it. Parts the
	// database does not report (OceanBase's full-text and spatial indexes)
	// are left out.
	Parts   []string
	Unique  bool
	Primary bool
	// Where is the predicate of a partial index.
	Where string
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
	// OnDelete and OnUpdate are the referential actions: one of the FK*
	// constants, empty when the database reports none.
	OnDelete string
	OnUpdate string
}

// Cascade is a foreign key of another relation that writes its rows when
// rows of the referenced relation change.
type Cascade struct {
	// Schema and Table name the referencing relation; Relation is its
	// relation key and Entity the entity exposing it ("" when none does),
	// both set at assembly.
	Schema   string
	Table    string
	Relation string
	Entity   string
	// Columns are the referenced columns of this relation; ForeignColumns
	// the referencing columns of the other, in the same order.
	Columns        []string
	ForeignColumns []string
	OnDelete       string
	OnUpdate       string
}

// Referential actions of a foreign key that write the referencing rows.
const (
	FKCascade    = "cascade"
	FKSetNull    = "set_null"
	FKSetDefault = "set_default"
)

// ReferentialWrite reports whether a referential action changes the
// referencing rows (cascade, set null or set default) rather than only
// checking them.
func ReferentialWrite(action string) bool {
	return action == FKCascade || action == FKSetNull || action == FKSetDefault
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
	// Indexes are the table's indexes as introspection reports them, for
	// administrators to see; the database plans access paths, not the
	// gateway. The unique ones are also Keys.
	Indexes     []Index
	Role        RoleAccess
	FieldAccess FieldAccess
	MCP         MCPFlags
	RowPolicies RowPolicies
	// TenantPolicy is ANDed for every principal and never merged across
	// grants; nil means the entity has no tenant boundary.
	TenantPolicy relalg.Predicate
	Relations    []Relationship
	// Derived marks a relation whose data comes from other relations (a view,
	// a materialized view or a foreign table): a write to any table of its
	// datasource may change it.
	Derived bool
	// Relation identifies the physical relation (set at assembly, see
	// RelationKey); at most one entity exposes a relation.
	Relation string
	// Affects lists the entities a procedure writes; empty means unknown.
	Affects []string
	// Cascades are the foreign keys of other relations whose referential
	// actions write their rows when this entity's rows are deleted or their
	// referenced columns updated (I-6).
	Cascades []Cascade
	// AllowCascade admits such writes without checking the caller's
	// permissions on the cascaded entities.
	AllowCascade bool
	// SideEffects reports triggers or rules on the relation: a write may
	// change other relations of the datasource.
	SideEffects bool
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

// RelationKey identifies the physical relation the entity exposes: Relation
// when assembly resolved it, otherwise its configured datasource, schema and
// source.
func (e Entity) RelationKey() string {
	if e.Relation != "" {
		return e.Relation
	}
	source := e.Source
	if source == "" {
		source = e.Name
	}
	return "datasource:" + e.DatasourceName() + "\x00\x00" + e.Schema + "\x00" + source
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

// IdentityKeys returns the column sets that identify at most one row,
// primary key first. enforced drops declared keys, as a write needs;
// inTransaction drops deferrable keys.
func (e Entity) IdentityKeys(enforced, inTransaction bool) [][]string {
	var out [][]string
	for _, primary := range []bool{true, false} {
		for _, k := range e.Keys {
			if k.Primary != primary || k.Reason != "" || enforced && k.Declared || inTransaction && k.Deferrable {
				continue
			}
			out = append(out, k.Columns)
		}
	}
	return out
}

// CursorKey returns the key keyset pagination orders by: the primary key,
// otherwise the first identity key; nil when the entity has none.
func (e Entity) CursorKey() []string {
	if keys := e.IdentityKeys(false, false); len(keys) > 0 {
		return keys[0]
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
