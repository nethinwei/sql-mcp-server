package codegen

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// ErrUnsupportedExpr is returned when the renderer encounters an expression it
// cannot render.
var ErrUnsupportedExpr = errors.New("codegen: unsupported expression")

// ErrUnboundedWrite is an UPDATE or DELETE without a predicate, or an UPDATE
// assigning nothing: the tools never build one, and codegen refuses it too.
var ErrUnboundedWrite = errors.New("codegen: write without a predicate or an assignment")

// Kind identifies the execution class of a compiled statement.
type Kind string

const (
	KindRead      Kind = "read"
	KindAggregate Kind = "aggregate"
	KindWrite     Kind = "write"
	KindCall      Kind = "call"
)

// Compiled is the physical, executable product of a logical plan: the rendered
// SQL, bound args, and metadata consumed by the cost gate, cache, and store.
type Compiled struct {
	Expr           relalg.Expr
	Kind           Kind
	SQL            string
	Args           []any
	IsKeyPoint     bool     // every column of an identity key matched by equality (gate whitelist)
	PrimaryKey     []string // primary-key columns, when supplied via WithPrimaryKey
	ReadOnly       bool
	AffectedTables []string // tables touched by a write (cache invalidation)
	// capBase is the SQL of a rendered select before its LIMIT clause, the
	// last clause, and capLimit/capOffset are that clause's (capLimit 0:
	// none); see CapRows.
	capBase   string
	capLimit  int64
	capOffset int64
}

// CapRows returns c reading at most n rows, when codegen rendered c as a
// select: its LIMIT is tightened to n, or added after ORDER BY, so the cap
// keeps the order a wrapping query would not guarantee. It reports false for
// any other statement, or one whose SQL changed since, for the caller to cap
// otherwise.
func CapRows(c Compiled, n int64) (Compiled, bool) {
	if c.capBase == "" || n < 1 || c.SQL != c.capBase+limitClause(c.capLimit, c.capOffset) {
		return c, false
	}
	if c.capLimit >= 1 && c.capLimit <= n {
		return c, true
	}
	c.capLimit = n
	c.SQL = c.capBase + limitClause(n, c.capOffset)
	return c, true
}

// CompileOption configures Compile.
type CompileOption func(*compileConfig)

type compileConfig struct {
	pkCols           []string
	identityKeys     [][]string
	identitySet      bool
	maxINCardinality int
}

// WithPrimaryKey supplies the primary-key columns: returned by an insert that
// supports RETURNING, and the identity key unless WithIdentityKeys says
// otherwise.
func WithPrimaryKey(cols ...string) CompileOption {
	return func(c *compileConfig) { c.pkCols = cols }
}

// WithIdentityKeys supplies the column sets that identify at most one row
// (the primary key and qualifying unique keys) so Compile can detect a point
// lookup on any of them; none means no lookup is a point. Without it the
// primary key is the only one.
func WithIdentityKeys(keys ...[]string) CompileOption {
	return func(c *compileConfig) { c.identityKeys, c.identitySet = keys, true }
}

// WithMaxINCardinality sets the maximum accepted IN/NOT IN list size. Values
// must be positive; invalid values fail compilation rather than disabling the
// bound.
func WithMaxINCardinality(max int) CompileOption {
	return func(c *compileConfig) { c.maxINCardinality = max }
}

// Renderer renders relalg expressions for a single dialect.
type Renderer struct {
	Dialect dialect.Dialect
}

// Compile renders e into a Compiled value. It applies inline lightweight
// transforms (IsKeyPoint detection) and capability-driven rendering (e.g.
// RETURNING). It does not run a separate optimizer: the IR is deliberately
// limited (no join/subquery), so predicate pushdown has nothing to push over.
func (r Renderer) Compile(e relalg.Expr, opts ...CompileOption) (Compiled, error) {
	cfg := compileConfig{maxINCardinality: relalg.DefaultMaxINCardinality}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.maxINCardinality <= 0 {
		return Compiled{}, fmt.Errorf("%w: maximum must be positive", relalg.ErrINCardinality)
	}
	b := &builder{dialect: r.Dialect, maxINCardinality: cfg.maxINCardinality}
	if r.Dialect.Capabilities().Enforced(dialect.Returning) && len(cfg.pkCols) > 0 {
		b.returningCols = cfg.pkCols
	}
	if err := b.render(e); err != nil {
		return Compiled{}, err
	}
	c := Compiled{
		Expr:           e,
		Kind:           kindOf(e),
		SQL:            b.sql.String(),
		Args:           b.args,
		ReadOnly:       b.readOnly,
		AffectedTables: b.tables,
		capBase:        b.capBase,
		capLimit:       b.capLimit,
		capOffset:      b.capOffset,
	}
	c.PrimaryKey = cfg.pkCols
	keys := cfg.identityKeys
	if !cfg.identitySet && len(cfg.pkCols) > 0 {
		keys = [][]string{cfg.pkCols}
	}
	for _, key := range keys {
		if isKeyPoint(PredicateOf(e), key) {
			c.IsKeyPoint = true
			break
		}
	}
	return c, nil
}

func kindOf(e relalg.Expr) Kind {
	switch e.(type) {
	case relalg.Insert, relalg.Update, relalg.Delete:
		return KindWrite
	case relalg.Call:
		return KindCall
	}
	for ok := true; ok; e, ok = relalg.Input(e) {
		if _, aggregate := e.(relalg.Aggregate); aggregate {
			return KindAggregate
		}
	}
	return KindRead
}

// builder accumulates SQL text and bound args while tracking a placeholder
// counter for positional dialects ($1, $2, ...).
type builder struct {
	dialect          dialect.Dialect
	sql              strings.Builder
	args             []any
	ph               int
	readOnly         bool
	tables           []string
	returningCols    []string // appended to INSERT when the dialect supports RETURNING
	maxINCardinality int
	// capBase, capLimit and capOffset: see Compiled.
	capBase             string
	capLimit, capOffset int64
	// outputs, while HAVING and ORDER BY of an aggregate are rendered, are
	// the aggregate calls by Name (see relalg.AggCall), rendered in place of
	// the name.
	outputs map[string]string
	// root qualifies the columns of a read with joins or EXISTS (the alias
	// of its relation); joins are the aliases its joins name their columns
	// by (alias.column); scope, inside an EXISTS, is the alias of the
	// relation it reads, which its columns are of.
	root   string
	joins  map[string]bool
	scope  string
	exists int
}

// column is how a condition or an order term refers to name: an aggregate's
// call where it names one, else the column (see col).
func (b *builder) column(name string) string {
	if expr, ok := b.outputs[name]; ok && b.scope == "" {
		return expr
	}
	return b.col(name)
}

// col is the quoted column name refers to: of the EXISTS being rendered, of
// a join (alias.column), or of the read's own relation, qualified when the
// read joins or nests another.
func (b *builder) col(name string) string {
	if b.scope != "" {
		return b.qident(b.scope) + "." + b.qident(name)
	}
	if alias, column, ok := strings.Cut(name, "."); ok && b.joins[alias] {
		return b.qident(alias) + "." + b.qident(column)
	}
	if b.root != "" {
		return b.qident(b.root) + "." + b.qident(name)
	}
	return b.qident(name)
}

func (b *builder) placeholder() string {
	b.ph++
	return b.dialect.Placeholder(b.ph - 1)
}

func (b *builder) qident(name string) string {
	return b.dialect.QuoteIdent(name)
}

func (b *builder) qtable(r relalg.RelationRef) string {
	if r.Schema != "" {
		return b.qident(r.Schema) + "." + b.qident(r.Name)
	}
	return b.qident(r.Name)
}

func (b *builder) noteTable(name string) {
	for _, t := range b.tables {
		if t == name {
			return
		}
	}
	b.tables = append(b.tables, name)
}

// opSQL maps a relalg.Op to its SQL operator.
func opSQL(op relalg.Op) string {
	switch op {
	case relalg.OpEq:
		return "="
	case relalg.OpNe:
		return "<>"
	case relalg.OpGt:
		return ">"
	case relalg.OpGte:
		return ">="
	case relalg.OpLt:
		return "<"
	case relalg.OpLte:
		return "<="
	case relalg.OpLike:
		return "LIKE"
	}
	return ""
}

// toValues normalizes an IN-list value into []any.
func toValues(v any) ([]any, error) {
	switch vals := v.(type) {
	case []any:
		return vals, nil
	case nil:
		return nil, fmt.Errorf("codegen: IN value is nil")
	default:
		return nil, fmt.Errorf("codegen: IN value must be []any, got %T", v)
	}
}

// PredicateOf is the WHERE predicate of an expression: every Select
// predicate along a read chain, innermost first, as one And, or the
// predicate of an update or delete; nil when it has none.
func PredicateOf(e relalg.Expr) relalg.Predicate {
	var preds []relalg.Predicate
	for ok := true; ok; e, ok = relalg.Input(e) {
		switch n := e.(type) {
		case relalg.Select:
			preds = append(preds, n.Predicate)
		case relalg.Update:
			preds = append(preds, n.Predicate)
		case relalg.Delete:
			preds = append(preds, n.Predicate)
		}
	}
	switch len(preds) {
	case 0:
		return nil
	case 1:
		return preds[0]
	}
	slices.Reverse(preds)
	return relalg.And{Preds: preds}
}

// isKeyPoint reports whether p matches every column of key by equality.
func isKeyPoint(p relalg.Predicate, pkCols []string) bool {
	if len(pkCols) == 0 {
		return false
	}
	conds, pure := flattenAnd(p)
	if !pure || len(conds) < len(pkCols) {
		return false
	}
	seen := make(map[string]bool, len(pkCols))
	pkSet := make(map[string]bool, len(pkCols))
	for _, c := range pkCols {
		pkSet[c] = true
	}
	for _, c := range conds {
		if !pkSet[c.Field] {
			continue
		}
		if c.Op != relalg.OpEq {
			return false
		}
		seen[c.Field] = true
	}
	return len(seen) == len(pkCols)
}

// flattenAnd returns the leaf Conditions of an And-chain and whether the
// predicate is a pure conjunction of Conditions. A predicate containing Or
// or Not is not pure and can never be a primary-key point lookup, so
// the caller must treat !pure as "not a PK point" rather than silently
// dropping those branches (which would let `id=5 OR <expensive>` masquerade
// as a point read and bypass the cost gate).
func flattenAnd(p relalg.Predicate) (conds []relalg.Condition, pure bool) {
	switch pp := p.(type) {
	case nil:
		return nil, true
	case relalg.Condition:
		return []relalg.Condition{pp}, true
	case relalg.And:
		var out []relalg.Condition
		for _, q := range pp.Preds {
			cs, ok := flattenAnd(q)
			if !ok {
				return nil, false
			}
			out = append(out, cs...)
		}
		return out, true
	}
	return nil, false
}
