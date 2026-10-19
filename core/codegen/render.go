package codegen

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// render dispatches an expression to its renderer.
func (b *builder) render(e relalg.Expr) error {
	switch n := e.(type) {
	case relalg.Scan, relalg.Select, relalg.Project, relalg.Sort,
		relalg.Limit, relalg.Distinct, relalg.Aggregate, relalg.Having, relalg.Join:
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
	scanAlias  string
	joins      []relalg.Join
	wherePreds []relalg.Predicate
	cols       []relalg.ProjectItem
	order      []relalg.OrderTerm
	groupBy    []string
	aggregates []relalg.AggCall
	having     relalg.Predicate
	limit      int64
	offset     int64
	hasLimit   bool
	distinct   bool
}

func (b *builder) flatten(e relalg.Expr, ctx *selectCtx) error {
	switch n := e.(type) {
	case relalg.Scan:
		ctx.from, ctx.scanAlias = b.qtable(n.Relation), n.Alias
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
		if err := checkLimit(n); err != nil {
			return err
		}
		if err := b.flatten(n.Input, ctx); err != nil {
			return err
		}
		ctx.limit, ctx.offset, ctx.hasLimit = n.Count, n.Offset, true
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
	case relalg.Having:
		return b.flattenHaving(n, ctx)
	case relalg.Join:
		return b.flattenJoin(n, ctx)
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedExpr, e)
	}
	return nil
}

func (b *builder) flattenHaving(n relalg.Having, ctx *selectCtx) error {
	if _, ok := n.Input.(relalg.Aggregate); !ok || n.Predicate == nil {
		return fmt.Errorf("%w: HAVING needs an aggregate and a predicate", ErrUnsupportedExpr)
	}
	if err := b.flatten(n.Input, ctx); err != nil {
		return err
	}
	ctx.having = n.Predicate
	return nil
}

func (b *builder) renderSelect(e relalg.Expr) error {
	ctx := &selectCtx{limit: -1}
	if err := b.flatten(e, ctx); err != nil {
		return err
	}
	for _, p := range append(slices.Clone(ctx.wherePreds), ctx.having) {
		if err := b.validate(p); err != nil {
			return err
		}
	}
	b.qualify(ctx)
	if err := b.writeSelectClause(ctx); err != nil {
		return err
	}
	if err := b.writeFromClause(ctx); err != nil {
		return err
	}
	if err := b.writeWhereClause(ctx); err != nil {
		return err
	}
	b.writeGroupByClause(ctx)
	if err := b.withOutputs(ctx, func() error {
		if err := b.writeHavingClause(ctx); err != nil {
			return err
		}
		b.writeOrderByClause(ctx)
		return nil
	}); err != nil {
		return err
	}
	b.capBase = b.sql.String()
	if ctx.hasLimit {
		b.capLimit, b.capOffset = ctx.limit, ctx.offset
	}
	b.sql.WriteString(limitClause(b.capLimit, b.capOffset))
	return nil
}

func (b *builder) writeSelectClause(ctx *selectCtx) error {
	b.sql.WriteString("SELECT ")
	if ctx.distinct {
		b.sql.WriteString("DISTINCT ")
	}
	switch {
	case len(ctx.aggregates) > 0 || len(ctx.groupBy) > 0:
		cols, err := b.renderAggregateCols(ctx)
		if err != nil {
			return err
		}
		b.sql.WriteString(cols)
	case len(ctx.cols) > 0:
		b.sql.WriteString(b.renderCols(ctx.cols))
	case len(ctx.joins) > 0:
		b.sql.WriteString(b.qident(b.root) + ".*") // the read's own columns
	default:
		b.sql.WriteString("*")
	}
	return nil
}

func (b *builder) writeWhereClause(ctx *selectCtx) error {
	if len(ctx.wherePreds) == 0 {
		return nil
	}
	b.sql.WriteString(" WHERE ")
	var pred relalg.Predicate
	if len(ctx.wherePreds) == 1 {
		pred = ctx.wherePreds[0]
	} else {
		pred = relalg.And{Preds: ctx.wherePreds}
	}
	return b.renderPredicate(pred)
}

func (b *builder) writeGroupByClause(ctx *selectCtx) {
	if len(ctx.groupBy) == 0 {
		return
	}
	b.sql.WriteString(" GROUP BY ")
	b.sql.WriteString(b.renderColumns(ctx.groupBy))
}

// withOutputs renders fn with the aggregate's named calls in scope (see
// builder.outputs).
func (b *builder) withOutputs(ctx *selectCtx, fn func() error) error {
	if len(ctx.aggregates) == 0 {
		return fn()
	}
	b.outputs = map[string]string{}
	for _, a := range ctx.aggregates {
		if a.Name == "" {
			continue
		}
		expr, err := b.aggregateCall(a)
		if err != nil {
			return err
		}
		b.outputs[a.Name] = expr
	}
	defer func() { b.outputs = nil }()
	return fn()
}

func (b *builder) writeHavingClause(ctx *selectCtx) error {
	if ctx.having == nil {
		return nil
	}
	b.sql.WriteString(" HAVING ")
	return b.renderPredicate(ctx.having)
}

// writeOrderByClause orders by the terms; a nullable term first orders its
// NULLs last, the same on every database (they default differently).
func (b *builder) writeOrderByClause(ctx *selectCtx) {
	if len(ctx.order) == 0 {
		return
	}
	b.sql.WriteString(" ORDER BY ")
	for i, t := range ctx.order {
		if i > 0 {
			b.sql.WriteString(", ")
		}
		col := b.column(t.Field)
		if t.Nullable {
			b.sql.WriteString("CASE WHEN " + col + " IS NULL THEN 1 ELSE 0 END, ")
		}
		b.sql.WriteString(col)
		if strings.EqualFold(t.Dir, "desc") {
			b.sql.WriteString(" DESC")
		} else {
			b.sql.WriteString(" ASC")
		}
	}
}

// checkLimit refuses a limit below 1 or a negative offset: LIMIT -1 reads as
// no limit on some databases.
func checkLimit(n relalg.Limit) error {
	if n.Count < 1 || n.Offset < 0 {
		return fmt.Errorf("codegen: limit %d offset %d: limit must be positive and offset not negative",
			n.Count, n.Offset)
	}
	return nil
}

// limitClause renders LIMIT limit OFFSET offset; a limit below 1 is none.
func limitClause(limit, offset int64) string {
	if limit < 1 {
		return ""
	}
	clause := " LIMIT " + strconv.FormatInt(limit, 10)
	if offset > 0 {
		clause += " OFFSET " + strconv.FormatInt(offset, 10)
	}
	return clause
}

func (b *builder) renderCols(items []relalg.ProjectItem) string {
	parts := make([]string, len(items))
	for i, it := range items {
		s := b.col(it.Field)
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

func (b *builder) renderAggregateCols(ctx *selectCtx) (string, error) {
	var parts []string
	for _, g := range ctx.groupBy {
		parts = append(parts, b.col(g))
	}
	for _, a := range ctx.aggregates {
		call, err := b.aggregateCall(a)
		if err != nil {
			return "", err
		}
		parts = append(parts, call)
	}
	return strings.Join(parts, ", "), nil
}

// aggregateCall renders one aggregate call.
func (b *builder) aggregateCall(a relalg.AggCall) (string, error) {
	// Defense in depth: the aggregate function is whitelisted here even
	// though tool and MCP schema also validate it — a raw function name
	// must never be interpolated into SQL unchecked.
	if !a.Func.Valid() {
		return "", fmt.Errorf("%w: %q", relalg.ErrInvalidAggFunc, a.Func)
	}
	field := "*"
	if a.Field != "" {
		field = b.col(a.Field)
	}
	if a.Distinct {
		field = "DISTINCT " + field
	}
	return strings.ToUpper(string(a.Func)) + "(" + field + ")", nil
}

// validate checks a predicate once, before it is rendered: every operator is
// whitelisted and every IN list bounded (a raw operator must never reach SQL).
func (b *builder) validate(p relalg.Predicate) error {
	return relalg.ValidatePredicateWithMaxINCardinality(p, b.maxINCardinality)
}

func (b *builder) renderPredicate(p relalg.Predicate) error {
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
	case relalg.Exists:
		return b.renderExists(pp)
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
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedExpr, p)
	}
}

func (b *builder) renderCondition(c relalg.Condition) error {
	b.sql.WriteString(b.column(c.Field))
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
		if len(vals) > b.maxINCardinality {
			return fmt.Errorf("%w: got %d values, maximum %d",
				relalg.ErrINCardinality, len(vals), b.maxINCardinality)
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
		b.sql.WriteString(" ")
		b.sql.WriteString(opSQL(c.Op))
		b.sql.WriteString(" ")
		b.sql.WriteString(b.placeholder())
		b.args = append(b.args, c.Value)
	}
	return nil
}

func (b *builder) renderInsert(n relalg.Insert) error {
	if len(n.Columns) == 0 || len(n.Tuples) == 0 {
		return fmt.Errorf("codegen: insert needs columns and at least one tuple")
	}
	for _, tup := range n.Tuples {
		if len(tup) != len(n.Columns) {
			return fmt.Errorf("codegen: insert tuple has %d values for %d columns", len(tup), len(n.Columns))
		}
	}
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
	if len(n.Set) == 0 || n.Predicate == nil {
		return ErrUnboundedWrite
	}
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
	return b.renderWhere(n.Predicate)
}

func (b *builder) renderDelete(n relalg.Delete) error {
	if n.Predicate == nil {
		return ErrUnboundedWrite
	}
	b.noteTable(n.Target.Name)
	b.sql.WriteString("DELETE FROM ")
	b.sql.WriteString(b.qtable(n.Target))
	return b.renderWhere(n.Predicate)
}

func (b *builder) renderWhere(p relalg.Predicate) error {
	if err := b.validate(p); err != nil {
		return err
	}
	b.sql.WriteString(" WHERE ")
	return b.renderPredicate(p)
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
