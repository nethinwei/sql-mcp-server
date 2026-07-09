package codegen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/relalg"
)

// render dispatches an expression to its renderer.
func (b *builder) render(e relalg.Expr) error {
	switch n := e.(type) {
	case relalg.Scan, relalg.Select, relalg.Project, relalg.Sort,
		relalg.Limit, relalg.Distinct, relalg.Aggregate:
		b.readOnly = true
		return b.renderSelect(n)
	case relalg.Insert:
		return b.renderInsert(n)
	case relalg.Update:
		return b.renderUpdate(n)
	case relalg.Delete:
		return b.renderDelete(n)
	case relalg.Call:
		return b.renderCall(n)
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedExpr, e)
	}
}

// selectCtx accumulates the flattened parts of a linear read chain.
type selectCtx struct {
	from       string
	wherePreds []relalg.Predicate
	cols       []relalg.ProjectItem
	order      []relalg.OrderTerm
	groupBy    []string
	aggregates []relalg.AggCall
	limit      int64
	offset     int64
	hasLimit   bool
	distinct   bool
}

func (b *builder) flatten(e relalg.Expr, ctx *selectCtx) error {
	switch n := e.(type) {
	case relalg.Scan:
		ctx.from = b.qtable(n.Relation)
		if n.Alias != "" {
			ctx.from += " AS " + b.qident(n.Alias)
		}
		b.noteTable(n.Relation.Name)
	case relalg.Select:
		if err := b.flatten(n.Input, ctx); err != nil {
			return err
		}
		ctx.wherePreds = append(ctx.wherePreds, n.Predicate)
	case relalg.Project:
		if err := b.flatten(n.Input, ctx); err != nil {
			return err
		}
		ctx.cols = n.Items
	case relalg.Sort:
		if err := b.flatten(n.Input, ctx); err != nil {
			return err
		}
		ctx.order = n.OrderBy
	case relalg.Limit:
		if err := b.flatten(n.Input, ctx); err != nil {
			return err
		}
		ctx.limit = n.Count
		ctx.offset = n.Offset
		ctx.hasLimit = true
	case relalg.Distinct:
		if err := b.flatten(n.Input, ctx); err != nil {
			return err
		}
		ctx.distinct = true
	case relalg.Aggregate:
		if err := b.flatten(n.Input, ctx); err != nil {
			return err
		}
		ctx.groupBy = n.GroupBy
		ctx.aggregates = n.Aggregates
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedExpr, e)
	}
	return nil
}

func (b *builder) renderSelect(e relalg.Expr) error {
	ctx := &selectCtx{limit: -1}
	if err := b.flatten(e, ctx); err != nil {
		return err
	}
	b.sql.WriteString("SELECT ")
	if ctx.distinct {
		b.sql.WriteString("DISTINCT ")
	}
	switch {
	case len(ctx.aggregates) > 0 || len(ctx.groupBy) > 0:
		b.sql.WriteString(b.renderAggregateCols(ctx))
	case len(ctx.cols) > 0:
		b.sql.WriteString(b.renderCols(ctx.cols))
	default:
		b.sql.WriteString("*")
	}
	b.sql.WriteString(" FROM ")
	b.sql.WriteString(ctx.from)
	if len(ctx.wherePreds) > 0 {
		b.sql.WriteString(" WHERE ")
		var pred relalg.Predicate
		if len(ctx.wherePreds) == 1 {
			pred = ctx.wherePreds[0]
		} else {
			pred = relalg.And{Preds: ctx.wherePreds}
		}
		if err := b.renderPredicate(pred); err != nil {
			return err
		}
	}
	if len(ctx.groupBy) > 0 {
		b.sql.WriteString(" GROUP BY ")
		b.sql.WriteString(b.renderFieldList(ctx.groupBy))
	}
	if len(ctx.order) > 0 {
		b.sql.WriteString(" ORDER BY ")
		for i, t := range ctx.order {
			if i > 0 {
				b.sql.WriteString(", ")
			}
			b.sql.WriteString(b.qident(t.Field))
			if strings.EqualFold(t.Dir, "desc") {
				b.sql.WriteString(" DESC")
			} else {
				b.sql.WriteString(" ASC")
			}
		}
	}
	if ctx.hasLimit {
		b.sql.WriteString(" LIMIT ")
		b.sql.WriteString(strconv.FormatInt(ctx.limit, 10))
		if ctx.offset > 0 {
			b.sql.WriteString(" OFFSET ")
			b.sql.WriteString(strconv.FormatInt(ctx.offset, 10))
		}
	}
	return nil
}

func (b *builder) renderCols(items []relalg.ProjectItem) string {
	parts := make([]string, len(items))
	for i, it := range items {
		s := b.qident(it.Field)
		if it.Alias != "" {
			s += " AS " + b.qident(it.Alias)
		}
		parts[i] = s
	}
	return strings.Join(parts, ", ")
}

func (b *builder) renderFieldList(fields []string) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = b.qident(f)
	}
	return strings.Join(parts, ", ")
}

func (b *builder) renderAggregateCols(ctx *selectCtx) string {
	var parts []string
	for _, g := range ctx.groupBy {
		parts = append(parts, b.qident(g))
	}
	for _, a := range ctx.aggregates {
		field := "*"
		if a.Field != "" {
			field = b.qident(a.Field)
		}
		if a.Distinct {
			field = "DISTINCT " + field
		}
		parts = append(parts, strings.ToUpper(a.Func)+"("+field+")")
	}
	return strings.Join(parts, ", ")
}

func (b *builder) renderPredicate(p relalg.Predicate) error {
	if err := relalg.ValidatePredicate(p); err != nil {
		return err
	}
	switch pp := p.(type) {
	case nil:
		return nil
	case relalg.Condition:
		return b.renderCondition(pp)
	case relalg.And:
		b.sql.WriteString("(")
		for i, q := range pp.Preds {
			if i > 0 {
				b.sql.WriteString(" AND ")
			}
			if err := b.renderPredicate(q); err != nil {
				return err
			}
		}
		b.sql.WriteString(")")
		return nil
	case relalg.Or:
		b.sql.WriteString("(")
		for i, q := range pp.Preds {
			if i > 0 {
				b.sql.WriteString(" OR ")
			}
			if err := b.renderPredicate(q); err != nil {
				return err
			}
		}
		b.sql.WriteString(")")
		return nil
	case relalg.Not:
		b.sql.WriteString("NOT ")
		return b.renderPredicate(pp.P)
	case relalg.IsNull:
		b.sql.WriteString(b.qident(pp.Field))
		if pp.Not {
			b.sql.WriteString(" IS NOT NULL")
		} else {
			b.sql.WriteString(" IS NULL")
		}
		return nil
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedExpr, p)
	}
}

func (b *builder) renderCondition(c relalg.Condition) error {
	b.sql.WriteString(b.qident(c.Field))
	switch c.Op {
	case relalg.OpIsNull:
		b.sql.WriteString(" IS NULL")
	case relalg.OpIsNotNull:
		b.sql.WriteString(" IS NOT NULL")
	case relalg.OpIn, relalg.OpNotIn:
		vals, err := toValues(c.Value)
		if err != nil {
			return err
		}
		if len(vals) == 0 {
			return fmt.Errorf("codegen: empty IN list")
		}
		if c.Op == relalg.OpNotIn {
			b.sql.WriteString(" NOT")
		}
		b.sql.WriteString(" IN (")
		for i, v := range vals {
			if i > 0 {
				b.sql.WriteString(", ")
			}
			b.sql.WriteString(b.placeholder())
			b.args = append(b.args, v)
		}
		b.sql.WriteString(")")
	default:
		b.sql.WriteString(" " + opSQL(c.Op) + " ")
		b.sql.WriteString(b.placeholder())
		b.args = append(b.args, c.Value)
	}
	return nil
}

func (b *builder) renderInsert(n relalg.Insert) error {
	b.noteTable(n.Target.Name)
	b.sql.WriteString("INSERT INTO ")
	b.sql.WriteString(b.qtable(n.Target))
	if len(n.Columns) > 0 {
		b.sql.WriteString(" (")
		b.sql.WriteString(b.renderFieldList(n.Columns))
		b.sql.WriteString(")")
	}
	b.sql.WriteString(" VALUES ")
	for i, tup := range n.Tuples {
		if i > 0 {
			b.sql.WriteString(", ")
		}
		b.sql.WriteString("(")
		for j, v := range tup {
			if j > 0 {
				b.sql.WriteString(", ")
			}
			b.sql.WriteString(b.placeholder())
			b.args = append(b.args, v)
		}
		b.sql.WriteString(")")
	}
	if len(b.returningCols) > 0 {
		b.sql.WriteString(" RETURNING ")
		b.sql.WriteString(b.renderFieldList(b.returningCols))
	}
	return nil
}

func (b *builder) renderUpdate(n relalg.Update) error {
	b.noteTable(n.Target.Name)
	b.sql.WriteString("UPDATE ")
	b.sql.WriteString(b.qtable(n.Target))
	b.sql.WriteString(" SET ")
	for i, s := range n.Set {
		if i > 0 {
			b.sql.WriteString(", ")
		}
		b.sql.WriteString(b.qident(s.Field))
		b.sql.WriteString(" = ")
		b.sql.WriteString(b.placeholder())
		b.args = append(b.args, s.Value)
	}
	if n.Predicate != nil {
		b.sql.WriteString(" WHERE ")
		if err := b.renderPredicate(n.Predicate); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) renderDelete(n relalg.Delete) error {
	b.noteTable(n.Target.Name)
	b.sql.WriteString("DELETE FROM ")
	b.sql.WriteString(b.qtable(n.Target))
	if n.Predicate != nil {
		b.sql.WriteString(" WHERE ")
		if err := b.renderPredicate(n.Predicate); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) renderCall(n relalg.Call) error {
	b.noteTable(n.Procedure.Name)
	b.sql.WriteString("CALL ")
	b.sql.WriteString(b.qtable(n.Procedure))
	b.sql.WriteString("(")
	for i, a := range n.Args {
		if i > 0 {
			b.sql.WriteString(", ")
		}
		b.sql.WriteString(b.placeholder())
		b.args = append(b.args, a)
	}
	b.sql.WriteString(")")
	return nil
}
