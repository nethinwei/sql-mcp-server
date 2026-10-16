package tool

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

type readPlan struct {
	entityPlan
	projectionFields []string
	full             relalg.Predicate
	keysetCols       []string // ORDER BY for keyset pagination
	order            readOrder
	grant            grant
}

func runRead(ctx context.Context, tc Context, in readInput) (Result, error) {
	ctx, tc, end := beginCall(ctx, tc, true, in.Transaction)
	defer end()
	start := time.Now()
	plan, err := prepareReadPlan(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	st, planTemplate, cacheKey, err := compileReadQuery(ctx, plan, in)
	if err != nil {
		return Result{}, err
	}
	var stamp uint64 // invalidations before the query starts, see cache.Cache.Stamp
	if plan.tc.Cache != nil {
		stamp = plan.tc.Cache.Stamp(cacheKey.Database)
	}
	if cached, err, ok := readCacheHit(ctx, plan.tc, in, cacheKey); ok {
		return cached, err
	}
	st.keep = expandJoinColumns(plan.res.Entity, in.Expand)
	got, err := st.query(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := expandRows(ctx, plan.tc, plan.res.Entity, got, in.Expand); err != nil {
		return Result{}, err
	}
	trimExpandedJoinFields(plan.res.Entity, in, got.rows)
	next := nextCursor(plan.order, plan.res, in, got.rows)
	trimOrderFields(plan.order, plan.res, in, got.rows)
	recordReadFeedback(ctx, plan.tc, in.Entity, st.sql.Compiled, planTemplate, st.sql.plan,
		int64(len(got.rows)), time.Since(start))
	if next == nil {
		storeReadCache(ctx, plan.tc, in, cacheKey, stamp, got.rows)
	}
	returned, _ := plan.tc.results.counted()
	return Result{Content: got.rows, ReturnedRows: returned, EstimatedScannedRows: st.sql.estimatedRows(),
		NextCursor: next}, nil
}

func prepareReadPlan(ctx context.Context, tc Context, in readInput) (readPlan, error) {
	ep, pred, err := planEntity(ctx, tc, in.Entity, entity.ActionRead, in.Filter)
	if err != nil {
		return readPlan{}, err
	}
	res, tc := ep.res, ep.tc
	readFields, projectionFields, err := readFieldNames(res, in)
	if err != nil {
		return readPlan{}, err
	}
	if err := validateFields(res, readFields...); err != nil {
		return readPlan{}, err
	}
	predicateFields := append(filterFields(in.Filter), sortedKeys(in.Cursor)...)
	if err := validateUnmaskedFields(res, predicateFields...); err != nil {
		return readPlan{}, err
	}
	req := rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionRead,
		Fields: projectionFields, ReadFields: readFields, Predicate: pred,
	}
	order, req, err := orderRead(ctx, tc, res, in, req)
	if err != nil {
		return readPlan{}, err
	}
	g, err := authorizeRequest(ctx, tc, req)
	if err != nil {
		return readPlan{}, err
	}
	keyset, keysetCols, err := readCursor(res.Entity, order, in)
	if err != nil {
		return readPlan{}, err
	}
	return readPlan{
		entityPlan: ep, projectionFields: req.Fields, order: order,
		full: andPreds(andPreds(pred, g.RowFilter), keyset), keysetCols: keysetCols, grant: g,
	}, nil
}

// orderRead plans in's orderBy and distinct on res and adds to req what they
// read: the ordered fields, and the ones nextCursor is built of, which the
// projection then carries too.
func orderRead(ctx context.Context, tc Context, res entity.Resolved, in readInput, req rbac.Request) (
	readOrder, rbac.Request, error,
) {
	if in.Distinct && (len(in.Fields) == 0 || len(in.Expand) > 0 || len(in.Cursor) > 0) {
		return readOrder{}, req, fmt.Errorf("%w: distinct needs fields, and takes no expand or cursor",
			ErrInvalidInput)
	}
	order, err := planReadOrder(res, in)
	if err != nil {
		return readOrder{}, req, err
	}
	// What the request reads stays read (and authorized) whatever the key's
	// fate: the fields asked for are added to it, the key's only when the
	// caller may read them.
	for _, f := range order.fields() {
		if !slices.Contains(order.keyFields, f) {
			req.ReadFields = append(slices.Clone(req.ReadFields), f)
		}
	}
	if !readsKey(ctx, tc, req, order) {
		order = order.withoutKey()
	}
	req.ReadFields = append(slices.Clone(req.ReadFields), order.keyFields...)
	if order.pageable && len(in.Fields) > 0 {
		for _, f := range order.fields() {
			if !slices.Contains(req.Fields, f) {
				req.Fields = append(slices.Clone(req.Fields), f)
			}
		}
	}
	return order, req, nil
}

// readCursor resumes a read after in.Cursor: by its order when it names
// one, else on the entity's cursor key.
func readCursor(e entity.Entity, order readOrder, in readInput) (relalg.Predicate, []string, error) {
	if len(in.OrderBy) == 0 {
		return cursorPredicate(e, in.Cursor)
	}
	if len(in.Cursor) == 0 {
		return nil, nil, nil
	}
	p, err := orderCursor(order, in.Cursor)
	return p, nil, err
}

// cursorPredicate resumes keyset pagination after cursor on the entity's
// cursor key. A cursor that cannot be honored is rejected rather than
// ignored, which would return the first page again.
func cursorPredicate(e entity.Entity, cursor map[string]any) (relalg.Predicate, []string, error) {
	if len(cursor) == 0 {
		return nil, nil, nil
	}
	key := e.CursorKey()
	if key == nil {
		return nil, nil, fmt.Errorf(
			"%w: entity %q has no primary or unique key for cursor paging",
			ErrInvalidInput,
			e.Name,
		)
	}
	keyset, cols := keysetAfter(key, cursor)
	if len(cols) != len(cursor) {
		return nil, nil, fmt.Errorf("%w: cursor must name a leading part of the key (%s)",
			ErrInvalidInput, strings.Join(key, ", "))
	}
	return keyset, cols, nil
}

func readFieldNames(res entity.Resolved, in readInput) ([]string, []string, error) {
	readFields := append(filterFields(in.Filter), in.Fields...)
	projectionFields := append([]string(nil), in.Fields...)
	for _, name := range in.Expand {
		relation, ok := relationshipByName(res.Entity, name)
		if !ok {
			return nil, nil, fmt.Errorf("%w: unknown relationship %q", ErrInvalidInput, name)
		}
		for local := range relation.JoinOn {
			readFields = append(readFields, local)
			if len(in.Fields) > 0 && !slices.Contains(projectionFields, local) {
				projectionFields = append(projectionFields, local)
			}
		}
	}
	return append(readFields, sortedKeys(in.Cursor)...), projectionFields, nil
}

func compileReadQuery(ctx context.Context, plan readPlan, in readInput) (statement, string, cache.Key, error) {
	tc, res := plan.tc, plan.res
	expr := buildReadExpression(res, in, plan)
	compiled, err := compile(tc, res.Entity, expr, identityKeys(tc, res.Entity, false))
	if err != nil {
		return statement{}, "", cache.Key{}, err
	}
	planTemplate := cost.Fingerprint(tc.DataSource, tc.Dialect.Name(), compiled)
	sql, err := gateSQL(ctx, tc, compiled)
	if err != nil {
		err = withIndexHints(ctx, tc, res.Entity, err)
		return statement{}, "", cache.Key{}, err
	}
	key := cache.Key{
		Database: physicalDatabase(res.Entity), Relation: cacheRelation(res.Entity),
		Datasource: res.Entity.DatasourceName(), Generation: tc.Generation,
		SQL:  sql.SQL + "\x00expand=" + strings.Join(in.Expand, ","),
		Args: argsKey(sql.Args), Scope: scopeKey(tc.Role, tc.Subject),
	}
	return newStatement(tc, res, plan.grant, sql), planTemplate, key, nil
}

func buildReadExpression(res entity.Resolved, in readInput, plan readPlan) relalg.Expr {
	scan := relalg.Scan{Relation: res.Entity.Ref()}
	var expr relalg.Expr = scan
	if plan.full != nil {
		expr = relalg.Select{Input: scan, Predicate: plan.full}
	}
	switch {
	case len(plan.order.terms) > 0:
		expr = relalg.Sort{Input: expr, OrderBy: plan.order.terms}
	case len(plan.keysetCols) > 0:
		order := make([]relalg.OrderTerm, len(plan.keysetCols))
		for i, c := range plan.keysetCols {
			order[i] = relalg.OrderTerm{Field: c, Dir: "asc"}
		}
		expr = relalg.Sort{Input: expr, OrderBy: order}
	}
	if len(plan.grant.Fields) > 0 {
		expr = relalg.Project{Input: expr, Items: relalg.ProjectFields(plan.grant.Fields)}
	}
	if in.Distinct {
		expr = relalg.Distinct{Input: expr}
	}
	if in.Limit > 0 {
		expr = relalg.Limit{Input: expr, Count: in.Limit, Offset: in.Offset}
	}
	return expr
}

func readCacheHit(ctx context.Context, tc Context, in readInput, key cache.Key) (Result, error, bool) {
	if tc.Cache == nil || in.Transaction != "" || len(in.Expand) > 0 || tc.followsWrite {
		return Result{}, nil, false
	}
	cached, ok := tc.Cache.Get(ctx, key)
	if !ok {
		return Result{}, nil, false
	}
	for _, row := range cached {
		if err := tc.results.add(row); err != nil {
			return Result{}, err, true
		}
	}
	returned, _ := tc.results.counted()
	return Result{Content: cached, ReturnedRows: returned}, nil, true
}

func trimExpandedJoinFields(parent entity.Entity, in readInput, out []map[string]any) {
	if len(in.Fields) == 0 {
		return
	}
	for _, row := range out {
		for _, name := range in.Expand {
			relation, _ := relationshipByName(parent, name)
			for local := range relation.JoinOn {
				if !slices.Contains(in.Fields, local) {
					delete(row, local)
				}
			}
		}
	}
}

func storeReadCache(ctx context.Context, tc Context, in readInput, key cache.Key, stamp uint64, out []map[string]any) {
	if tc.Cache == nil || in.Transaction != "" || len(in.Expand) > 0 || tc.followsWrite {
		return
	}
	_, size := tc.results.counted() // a read cached has no expansion: its rows are the call's
	rowsAllowed := tc.CacheMaxEntryRows <= 0 || len(out) <= tc.CacheMaxEntryRows
	bytesAllowed := tc.CacheMaxEntryBytes <= 0 || size <= tc.CacheMaxEntryBytes
	if rowsAllowed && bytesAllowed {
		_ = tc.Cache.Set(ctx, key, out, stamp)
	}
}

// expandJoinColumns are the local columns the relationships expanded join
// on, whose values are kept before the rows are masked.
func expandJoinColumns(parent entity.Entity, names []string) []string {
	var columns []string
	for _, name := range names {
		relation, _ := relationshipByName(parent, name)
		for local := range relation.JoinOn {
			if !slices.Contains(columns, local) {
				columns = append(columns, local)
			}
		}
	}
	return columns
}

func expandRows(ctx context.Context, tc Context, parent entity.Entity, parents queried, names []string) error {
	for _, name := range names {
		if err := expandOneRelationship(ctx, tc, parent, parents, name); err != nil {
			return err
		}
	}
	return nil
}

func expandOneRelationship(ctx context.Context, tc Context, parent entity.Entity, parents queried, name string) error {
	relation, _ := relationshipByName(parent, name)
	join := relationJoin(relation)
	target, err := resolveDMLEntity(tc, relation.Target)
	if err != nil {
		return err
	}
	if err := validateFields(target, join.target...); err != nil {
		return err
	}
	if target.Entity.DataSource != parent.DataSource {
		return fmt.Errorf("%w: cross-datasource relationship %q", ErrInvalidInput, name)
	}
	targetTC, err := routeEntity(ctx, tc, target.Entity, entity.ActionRead)
	if err != nil {
		return err
	}
	tuples := uniqueJoinTuples(parents.kept, join.local)
	grouped := map[string][]map[string]any{}
	if len(tuples) > 0 {
		grouped, err = queryExpandedChildren(ctx, tc, targetTC, target, relation, join, tuples)
		if err != nil {
			return err
		}
	}
	attachExpandedChildren(parents, name, join, relation, grouped)
	return nil
}

// joinColumns pairs a relationship's local and target columns in a fixed
// order.
type joinColumns struct{ local, target []string }

func relationJoin(relation entity.Relationship) joinColumns {
	var join joinColumns
	join.local = slices.Sorted(maps.Keys(relation.JoinOn))
	for _, local := range join.local {
		join.target = append(join.target, relation.JoinOn[local])
	}
	return join
}

// joinKey identifies a tuple of join values regardless of how the driver
// typed them (an int4 foreign key matches an int8 key; text may arrive as
// bytes); ok is false when a value is NULL, which matches nothing.
func joinKey(values ...any) (key string, ok bool) {
	parts := make([]string, len(values))
	for i, v := range values {
		// Each part is quoted, so no value can contain the separator.
		switch x := v.(type) {
		case nil:
			return "", false
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			parts[i] = "n:" + fmt.Sprint(x)
		case []byte:
			parts[i] = "s:" + strconv.Quote(string(x))
		case string:
			parts[i] = "s:" + strconv.Quote(x)
		default:
			parts[i] = fmt.Sprintf("%T:%q", x, fmt.Sprint(x))
		}
	}
	return strings.Join(parts, ","), true
}

func rowJoinKey(row map[string]any, columns []string) (string, bool) {
	values := make([]any, len(columns))
	for i, c := range columns {
		values[i] = row[c]
	}
	return joinKey(values...)
}

// uniqueJoinTuples lists the distinct non-NULL join tuples of rows.
func uniqueJoinTuples(rows []map[string]any, columns []string) [][]any {
	tuples := make([][]any, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		key, ok := rowJoinKey(row, columns)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		tuple := make([]any, len(columns))
		for i, c := range columns {
			tuple[i] = row[c]
		}
		tuples = append(tuples, tuple)
	}
	return tuples
}

func queryExpandedChildren(
	ctx context.Context,
	tc Context,
	targetTC Context,
	target entity.Resolved,
	relation entity.Relationship,
	join joinColumns,
	tuples [][]any,
) (map[string][]map[string]any, error) {
	grouped := map[string][]map[string]any{}
	// A batch binds len(tuples)*len(columns) values: keep it within the IN
	// bound for a single column and the same number of values otherwise.
	batchSize := max(1, effectiveMaxIN(targetTC)/len(join.target))
	for start := 0; start < len(tuples); start += batchSize {
		stop := min(start+batchSize, len(tuples))
		st, err := compileExpandBatch(ctx, tc, targetTC, target, relation, join, tuples[start:stop])
		if err != nil {
			return nil, err
		}
		children, err := st.query(ctx)
		if err != nil {
			return nil, err
		}
		for i, child := range children.rows {
			if key, ok := rowJoinKey(children.kept[i], join.target); ok {
				grouped[key] = append(grouped[key], child)
			}
		}
	}
	return grouped, nil
}

// joinPredicate selects the target rows matching tuples: an IN list for a
// single column, otherwise one conjunction of equalities per tuple.
func joinPredicate(columns []string, tuples [][]any) relalg.Predicate {
	if len(columns) == 1 {
		values := make([]any, len(tuples))
		for i, t := range tuples {
			values[i] = t[0]
		}
		return relalg.Condition{Field: columns[0], Op: relalg.OpIn, Value: values}
	}
	ors := make([]relalg.Predicate, len(tuples))
	for i, t := range tuples {
		ands := make([]relalg.Predicate, len(columns))
		for j, c := range columns {
			ands[j] = relalg.Condition{Field: c, Op: relalg.OpEq, Value: t[j]}
		}
		ors[i] = relalg.And{Preds: ands}
	}
	if len(ors) == 1 {
		return ors[0]
	}
	return relalg.Or{Preds: ors}
}

func compileExpandBatch(
	ctx context.Context,
	tc Context,
	targetTC Context,
	target entity.Resolved,
	relation entity.Relationship,
	join joinColumns,
	tuples [][]any,
) (statement, error) {
	predicate := joinPredicate(join.target, tuples)
	g, err := authorizeRequest(ctx, targetTC, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: relation.Target,
		Action: entity.ActionRead, ReadFields: join.target, Predicate: predicate,
	})
	if err != nil {
		return statement{}, err
	}
	full := andPreds(predicate, g.RowFilter)
	scan := relalg.Scan{Relation: target.Entity.Ref()}
	expr := relalg.Expr(relalg.Select{Input: scan, Predicate: full})
	if len(g.Fields) > 0 {
		expr = relalg.Project{Input: expr, Items: relalg.ProjectFields(g.Fields)}
	}
	compiled, err := compile(targetTC, target.Entity, expr, identityKeys(tc, target.Entity, false),
		codegen.WithMaxINCardinality(max(len(tuples), effectiveMaxIN(targetTC))))
	if err != nil {
		return statement{}, err
	}
	sql, err := gateSQL(ctx, targetTC, compiled)
	if err != nil {
		return statement{}, err
	}
	st := newStatement(targetTC, target, g, sql)
	st.keep = join.target
	return st, nil
}

func attachExpandedChildren(
	parents queried,
	name string,
	join joinColumns,
	relation entity.Relationship,
	grouped map[string][]map[string]any,
) {
	for i, row := range parents.rows {
		var children []map[string]any
		if key, ok := rowJoinKey(parents.kept[i], join.local); ok {
			children = grouped[key]
		}
		switch {
		case relation.Single() && len(children) > 0:
			row[name] = children[0]
		case relation.Single():
			row[name] = nil
		case children == nil:
			row[name] = []map[string]any{}
		default:
			row[name] = children
		}
	}
}
