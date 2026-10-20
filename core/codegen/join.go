package codegen

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// A read joins relations at most one row of which matches each of its rows
// (relalg.Join), and filters by relations some row of which matches
// (relalg.Exists). Every column is then qualified: the read's own by its
// relation's alias (rootAlias, unless its scan names one), a join's by the
// join's alias, an EXISTS's by its own.

// rootAlias names the read's own relation when it joins or nests another.
const rootAlias = "_"

func (b *builder) flattenJoin(n relalg.Join, ctx *selectCtx) error {
	if n.Alias == "" || n.Alias == rootAlias || len(n.On) == 0 {
		return fmt.Errorf("%w: a join needs its own alias and its columns", ErrUnsupportedExpr)
	}
	if err := b.flatten(n.Input, ctx); err != nil {
		return err
	}
	if b.joins[n.Alias] {
		return fmt.Errorf("%w: alias %q joined twice", ErrUnsupportedExpr, n.Alias)
	}
	if b.joins == nil {
		b.joins = map[string]bool{}
	}
	b.joins[n.Alias] = true
	ctx.joins = append(ctx.joins, n)
	return nil
}

// qualify decides whether the read qualifies its columns: when it joins, or
// filters by EXISTS.
func (b *builder) qualify(ctx *selectCtx) {
	nested := len(ctx.joins) > 0 || relalg.HasExists(ctx.having)
	for _, p := range ctx.wherePreds {
		nested = nested || relalg.HasExists(p)
	}
	switch {
	case !nested:
	case ctx.scanAlias != "":
		b.root = ctx.scanAlias
	default:
		b.root = rootAlias
	}
}

func (b *builder) writeFromClause(ctx *selectCtx) error {
	b.sql.WriteString(" FROM " + ctx.from)
	if alias := cmp.Or(ctx.scanAlias, b.root); alias != "" {
		b.sql.WriteString(" AS " + b.qident(alias))
	}
	for _, j := range ctx.joins {
		if err := relalg.ValidatePredicateWithMaxINCardinality(j.RightPredicate, b.maxINCardinality); err != nil {
			return err
		}
		b.sql.WriteString(" LEFT JOIN " + b.qtable(j.Right) + " AS " + b.qident(j.Alias) + " ON ")
		if err := b.renderOn(j.Alias, j.On, j.RightPredicate); err != nil {
			return err
		}
	}
	return nil
}

// renderOn renders the condition joining the relation of alias: its columns
// equal to the outer ones, and its own predicate, rendered as its columns.
func (b *builder) renderOn(alias string, on []relalg.JoinPair, pred relalg.Predicate) error {
	parts := make([]string, len(on))
	for i, p := range on {
		parts[i] = b.qident(alias) + "." + b.qident(p.Right) + " = " + b.col(p.Left)
	}
	b.sql.WriteString(strings.Join(parts, " AND "))
	if pred == nil {
		return nil
	}
	b.sql.WriteString(" AND ")
	outer := b.scope
	b.scope = alias
	defer func() { b.scope = outer }()
	return b.renderPredicate(pred)
}

// renderExists renders an EXISTS over its own alias.
func (b *builder) renderExists(e relalg.Exists) error {
	if b.scope != "" {
		return fmt.Errorf("%w: EXISTS does not nest", ErrUnsupportedExpr)
	}
	b.exists++
	alias := "x" + strconv.Itoa(b.exists)
	b.sql.WriteString("EXISTS (SELECT 1 FROM " + b.qtable(e.Relation) + " AS " + b.qident(alias) + " WHERE ")
	if err := b.renderOn(alias, e.On, e.Predicate); err != nil {
		return err
	}
	b.sql.WriteString(")")
	return nil
}

// renderColumns renders the columns of a read (see col).
func (b *builder) renderColumns(fields []string) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = b.col(f)
	}
	return strings.Join(parts, ", ")
}
