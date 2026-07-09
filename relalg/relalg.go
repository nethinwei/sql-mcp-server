package relalg

// Op is a whitelisted comparison operator. User input never reaches SQL as a
// raw operator string, preventing injection.
type Op string

const (
	OpEq        Op = "eq"
	OpNe        Op = "ne"
	OpGt        Op = "gt"
	OpGte       Op = "gte"
	OpLt        Op = "lt"
	OpLte       Op = "lte"
	OpIn        Op = "in"
	OpNotIn     Op = "not_in"
	OpLike      Op = "like"
	OpIsNull    Op = "is_null"
	OpIsNotNull Op = "is_not_null"
)

// Valid reports whether op is a whitelisted operator.
func (o Op) Valid() bool {
	switch o {
	case OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNotIn, OpLike, OpIsNull, OpIsNotNull:
		return true
	}
	return false
}

// RelationRef names a base relation (table/view/procedure) by schema and name.
type RelationRef struct {
	Schema string
	Name   string
}

// ProjectItem is one column in a projection: the source field and an optional
// alias.
type ProjectItem struct {
	Field string
	Alias string
}

// AggCall is one aggregate in a γ node. Func is count|sum|avg|min|max.
type AggCall struct {
	Func     string
	Field    string
	Distinct bool
}

// OrderTerm is one column in a τ node. Dir is "asc" or "desc".
type OrderTerm struct {
	Field string
	Dir   string
}

// Tuple is a literal row of values, used in insert and IN-lists.
type Tuple []any

// SetItem is one field=value assignment in an update.
type SetItem struct {
	Field string
	Value any
}

// OnConflict configures insert-on-conflict behavior.
type OnConflict struct {
	Target    []string
	DoNothing bool
	Set       []SetItem
}

// Expr is a relational algebra expression (logical plan node). Sealed: external
// packages cannot implement it.
type Expr interface{ rel() }

// Scan is a base relation reference.
type Scan struct {
	Relation RelationRef
	Alias    string
}

// Select applies σ (filtering) to its input.
type Select struct {
	Input     Expr
	Predicate Predicate
}

// Project applies π (column picking / renaming) to its input.
type Project struct {
	Input Expr
	Items []ProjectItem
}

// Aggregate applies γ (grouping and aggregation) to its input.
type Aggregate struct {
	Input      Expr
	GroupBy    []string
	Aggregates []AggCall
}

// Sort applies τ (ordering) to its input.
type Sort struct {
	Input   Expr
	OrderBy []OrderTerm
}

// Limit bounds the row count (and optional offset) of its input.
type Limit struct {
	Input  Expr
	Count  int64
	Offset int64
}

// Distinct applies δ (deduplication) to its input.
type Distinct struct {
	Input Expr
}

// Values is a literal relation: named columns and rows. Used for insert.
type Values struct {
	Columns []string
	Tuples  []Tuple
}

// Insert is the relational assignment for create. Tuples supply the rows.
type Insert struct {
	Target   RelationRef
	Columns  []string
	Tuples   []Tuple
	Conflict *OnConflict
}

// Update is the relational assignment for update, constrained by a predicate.
type Update struct {
	Target    RelationRef
	Predicate Predicate
	Set       []SetItem
}

// Delete is the relational assignment for delete, constrained by a predicate.
type Delete struct {
	Target    RelationRef
	Predicate Predicate
}

// Marker methods seal the Expr interface.
func (Scan) rel()      {}
func (Select) rel()    {}
func (Project) rel()   {}
func (Aggregate) rel() {}
func (Sort) rel()      {}
func (Limit) rel()     {}
func (Distinct) rel()  {}
func (Values) rel()    {}
func (Insert) rel()    {}
func (Update) rel()    {}
func (Delete) rel()    {}

// Predicate is a parameterized boolean expression tree. Values bind to args at
// codegen time, never string-interpolated, preventing injection. Sealed.
type Predicate interface{ pred() }

// Condition is a leaf: field op value.
type Condition struct {
	Field string
	Op    Op
	Value any
}

// And combines predicates conjunctively.
type And struct{ Preds []Predicate }

// Or combines predicates disjunctively.
type Or struct{ Preds []Predicate }

// Not negates a predicate.
type Not struct{ P Predicate }

// IsNull tests null/non-null of a field.
type IsNull struct {
	Field string
	Not   bool
}

// Marker methods seal the Predicate interface.
func (Condition) pred() {}
func (And) pred()       {}
func (Or) pred()        {}
func (Not) pred()       {}
func (IsNull) pred()    {}
