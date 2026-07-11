package codegen

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nethinwei/sql-mcp-server/dialect"
	"github.com/nethinwei/sql-mcp-server/relalg"
)

// ErrUnsupportedExpr is returned when the renderer encounters an expression it
// cannot render.
var ErrUnsupportedExpr = errors.New("codegen: unsupported expression")

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
	IsPKPoint      bool // all primary-key columns matched by equality (gate whitelist)
	ReadOnly       bool
	AffectedTables []string // tables touched by a write (cache invalidation)
}

// CompileOption configures Compile.
type CompileOption func(*compileConfig)

type compileConfig struct {
	pkCols           []string
	maxINCardinality int
}

// WithPrimaryKey supplies primary-key columns so Compile can detect a primary
// key point lookup and set Compiled.IsPKPoint.
func WithPrimaryKey(cols ...string) CompileOption {
	return func(c *compileConfig) { c.pkCols = cols }
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

// NewRenderer returns a Renderer for the given dialect.
func NewRenderer(d dialect.Dialect) Renderer {
	return Renderer{Dialect: d}
}

// Compile renders e into a Compiled value. It applies inline lightweight
// transforms (IsPKPoint detection) and capability-driven rendering (e.g.
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
	if r.Dialect.Capabilities().Returning && len(cfg.pkCols) > 0 {
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
	}
	if len(cfg.pkCols) > 0 {
		c.IsPKPoint = isPKPoint(predicateOf(e), cfg.pkCols)
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
	if containsAggregate(e) {
		return KindAggregate
	}
	return KindRead
}

func containsAggregate(e relalg.Expr) bool {
	switch n := e.(type) {
	case relalg.Aggregate:
		return true
	case relalg.Select:
		return containsAggregate(n.Input)
	case relalg.Project:
		return containsAggregate(n.Input)
	case relalg.Sort:
		return containsAggregate(n.Input)
	case relalg.Limit:
		return containsAggregate(n.Input)
	case relalg.Distinct:
		return containsAggregate(n.Input)
	default:
		return false
	}
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

// predicateOf extracts the combined WHERE predicate from a read expr by
// collecting every Select predicate along the chain into an And.
func predicateOf(e relalg.Expr) relalg.Predicate {
	var preds []relalg.Predicate
	var walk func(relalg.Expr)
	walk = func(node relalg.Expr) {
		switch n := node.(type) {
		case relalg.Select:
			walk(n.Input)
			preds = append(preds, n.Predicate)
		case relalg.Project:
			walk(n.Input)
		case relalg.Sort:
			walk(n.Input)
		case relalg.Limit:
			walk(n.Input)
		case relalg.Distinct:
			walk(n.Input)
		case relalg.Aggregate:
			walk(n.Input)
		case relalg.Update:
			preds = append(preds, n.Predicate)
		case relalg.Delete:
			preds = append(preds, n.Predicate)
		}
	}
	walk(e)
	if len(preds) == 0 {
		return nil
	}
	if len(preds) == 1 {
		return preds[0]
	}
	return relalg.And{Preds: preds}
}

// isPKPoint reports whether p matches every primary-key column by equality.
func isPKPoint(p relalg.Predicate, pkCols []string) bool {
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
// predicate is a pure conjunction of Conditions. A predicate containing Or,
// Not, or IsNull is not pure and can never be a primary-key point lookup, so
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
