package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

type readPlan struct {
	res              entity.Resolved
	tc               Context
	projectionFields []string
	full             relalg.Predicate
	keysetCols       []string // ORDER BY for keyset pagination
	dec              rbac.Decision
}

func runRead(ctx context.Context, tc Context, in readInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc, 0)
	defer cancel()
	tc.Transaction = in.Transaction
	start := time.Now()
	plan, err := prepareReadPlan(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	compiled, planTemplate, estimatedPlan, cacheKey, err := compileReadQuery(ctx, plan.tc, plan.res, in, plan)
	if err != nil {
		return Result{}, err
	}
	if cached, err, ok := readCacheHit(ctx, plan.tc, in, cacheKey); ok {
		return cached, err
	}
	out, returned, err := queryReadRows(ctx, plan.tc, compiled)
	if err != nil {
		return Result{}, err
	}
	out, returned, err = finalizeReadRows(ctx, plan.tc, plan.res, in, out, &returned)
	if err != nil {
		return Result{}, err
	}
	recordReadFeedback(
		ctx,
		plan.tc,
		in.Entity,
		compiled,
		planTemplate,
		estimatedPlan,
		int64(len(out)),
		time.Since(start),
	)
	storeReadCache(ctx, plan.tc, in, cacheKey, out)
	estimatedRows := int64(0)
	if estimatedPlan != nil {
		estimatedRows = estimatedPlan.EstimatedRows
	}
	return Result{Content: out, ReturnedRows: returned, EstimatedScannedRows: estimatedRows}, nil
}

func prepareReadPlan(ctx context.Context, tc Context, in readInput) (readPlan, error) {
	res, err := resolveDMLEntity(tc, in.Entity)
	if err != nil {
		return readPlan{}, err
	}
	tc, err = routeEntity(tc, res.Entity, entity.ActionRead)
	if err != nil {
		return readPlan{}, err
	}
	pred, err := filterToPredicate(in.Filter, tc.MaxFilterConditions)
	if err != nil {
		return readPlan{}, err
	}
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
	dec, err := authorize(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionRead,
		Fields: projectionFields, ReadFields: readFields, Predicate: pred,
	})
	if err != nil {
		return readPlan{}, err
	}
	if !dec.Allowed {
		return readPlan{}, denyUnauthorized(dec)
	}
	keyset, keysetCols, err := cursorPredicate(res.Entity, in.Cursor)
	if err != nil {
		return readPlan{}, err
	}
	return readPlan{
		res: res, tc: tc, projectionFields: projectionFields,
		full: andPreds(andPreds(pred, dec.RowFilter), keyset), keysetCols: keysetCols, dec: dec,
	}, nil
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
		return nil, nil, fmt.Errorf("%w: entity %q has no primary or unique key for cursor paging", ErrInvalidInput, e.Name)
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

func compileReadQuery(
	ctx context.Context,
	tc Context,
	res entity.Resolved,
	in readInput,
	plan readPlan,
) (codegen.Compiled, string, *cost.Plan, cache.Key, error) {
	expr := buildReadExpression(res, in, plan)
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(expr,
		codegen.WithPrimaryKey(res.Entity.PrimaryKey()...),
		codegen.WithIdentityKeys(res.Entity.IdentityKeys(false, tc.Transaction != "")...),
		codegen.WithMaxINCardinality(effectiveMaxIN(tc)))
	if err != nil {
		return codegen.Compiled{}, "", nil, cache.Key{}, err
	}
	planTemplate := cost.Fingerprint(tc.DataSource, tc.Dialect.Name(), compiled)
	compiled, estimatedPlan, err := checkGateDetailed(ctx, tc, compiled)
	if err != nil {
		return codegen.Compiled{}, "", nil, cache.Key{}, err
	}
	key := cache.Key{
		Database: res.Entity.DatasourceName(), Relation: cacheRelation(res.Entity),
		SQL:  compiled.SQL + "\x00expand=" + strings.Join(in.Expand, ","),
		Args: argsKey(compiled.Args), Scope: scopeKey(tc.Role, tc.Subject),
	}
	return compiled, planTemplate, estimatedPlan, key, nil
}

func buildReadExpression(res entity.Resolved, in readInput, plan readPlan) relalg.Expr {
	scan := relalg.Scan{Relation: relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema}}
	var expr relalg.Expr = scan
	if plan.full != nil {
		expr = relalg.Select{Input: scan, Predicate: plan.full}
	}
	if len(plan.keysetCols) > 0 {
		order := make([]relalg.OrderTerm, len(plan.keysetCols))
		for i, c := range plan.keysetCols {
			order[i] = relalg.OrderTerm{Field: c, Dir: "asc"}
		}
		expr = relalg.Sort{Input: expr, OrderBy: order}
	}
	if len(plan.dec.Fields) > 0 {
		items := make([]relalg.ProjectItem, len(plan.dec.Fields))
		for i, f := range plan.dec.Fields {
			items[i] = relalg.ProjectItem{Field: f}
		}
		expr = relalg.Project{Input: expr, Items: items}
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
	returned := countReadRows(cached, in.Expand)
	if tc.BudgetLimits.MaxReturnedRows > 0 && returned > tc.BudgetLimits.MaxReturnedRows {
		return Result{}, budget.ErrExceeded, true
	}
	return Result{Content: cached, ReturnedRows: returned}, nil, true
}

func queryReadRows(ctx context.Context, tc Context, compiled codegen.Compiled) ([]map[string]any, int64, error) {
	rows, err := tc.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return nil, 0, WrapDBError(err)
	}
	out := make([]map[string]any, 0)
	var returned int64
	for row, err := range store.Iter(rows) {
		if err != nil {
			return nil, 0, WrapDBError(err)
		}
		returned++
		if tc.BudgetLimits.MaxReturnedRows > 0 && returned > tc.BudgetLimits.MaxReturnedRows {
			return nil, 0, budget.ErrExceeded
		}
		out = append(out, row)
	}
	return out, returned, nil
}

func finalizeReadRows(
	ctx context.Context,
	tc Context,
	res entity.Resolved,
	in readInput,
	out []map[string]any,
	returned *int64,
) ([]map[string]any, int64, error) {
	if len(in.Expand) > 0 {
		if err := expandRows(ctx, tc, res.Entity, out, in.Expand, returned); err != nil {
			return nil, 0, err
		}
	}
	trimExpandedJoinFields(res.Entity, in, out)
	if tc.Masker != nil {
		for _, row := range out {
			maskRow(tc.Masker, res.Entity.Attributes, row)
		}
	}
	return out, *returned, nil
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

func storeReadCache(ctx context.Context, tc Context, in readInput, key cache.Key, out []map[string]any) {
	if tc.Cache == nil || in.Transaction != "" || len(in.Expand) > 0 || tc.followsWrite {
		return
	}
	encoded, encodeErr := json.Marshal(out)
	rowsAllowed := tc.CacheMaxEntryRows <= 0 || len(out) <= tc.CacheMaxEntryRows
	bytesAllowed := tc.CacheMaxEntryBytes <= 0 || int64(len(encoded)) <= tc.CacheMaxEntryBytes
	if encodeErr == nil && rowsAllowed && bytesAllowed {
		_ = tc.Cache.Set(ctx, key, out)
	}
}

func expandRows(
	ctx context.Context,
	tc Context,
	parent entity.Entity,
	rows []map[string]any,
	names []string,
	returned *int64,
) error {
	for _, name := range names {
		if err := expandOneRelationship(ctx, tc, parent, rows, name, returned); err != nil {
			return err
		}
	}
	return nil
}

func expandOneRelationship(
	ctx context.Context,
	tc Context,
	parent entity.Entity,
	rows []map[string]any,
	name string,
	returned *int64,
) error {
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
	targetTC, err := routeEntity(tc, target.Entity, entity.ActionRead)
	if err != nil {
		return err
	}
	tuples := uniqueJoinTuples(rows, join.local)
	grouped := map[string][]map[string]any{}
	if len(tuples) > 0 {
		grouped, err = queryExpandedChildren(ctx, tc, targetTC, target, relation, join, tuples, returned)
		if err != nil {
			return err
		}
	}
	attachExpandedChildren(rows, name, join, relation, grouped)
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
	returned *int64,
) (map[string][]map[string]any, error) {
	grouped := map[string][]map[string]any{}
	// A batch binds len(tuples)*len(columns) values: keep it within the IN
	// bound for a single column and the same number of values otherwise.
	batchSize := max(1, effectiveMaxIN(targetTC)/len(join.target))
	for start := 0; start < len(tuples); start += batchSize {
		stop := min(start+batchSize, len(tuples))
		batch, err := queryExpandBatch(ctx, tc, targetTC, target, relation, join, tuples[start:stop], returned)
		if err != nil {
			return nil, err
		}
		for key, children := range batch {
			grouped[key] = append(grouped[key], children...)
		}
	}
	return grouped, nil
}

func queryExpandBatch(
	ctx context.Context,
	tc Context,
	targetTC Context,
	target entity.Resolved,
	relation entity.Relationship,
	join joinColumns,
	tuples [][]any,
	returned *int64,
) (map[string][]map[string]any, error) {
	compiled, err := compileExpandBatch(ctx, tc, targetTC, target, relation, join, tuples)
	if err != nil {
		return nil, err
	}
	resultRows, err := targetTC.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return nil, WrapDBError(err)
	}
	grouped := map[string][]map[string]any{}
	for child, iterErr := range store.Iter(resultRows) {
		if iterErr != nil {
			return nil, WrapDBError(iterErr)
		}
		if err := appendExpandedChild(tc, targetTC, target, join, child, grouped, returned); err != nil {
			return nil, err
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
) (codegen.Compiled, error) {
	predicate := joinPredicate(join.target, tuples)
	decision, err := authorize(ctx, targetTC, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: relation.Target,
		Action: entity.ActionRead, ReadFields: join.target, Predicate: predicate,
	})
	if err != nil {
		return codegen.Compiled{}, err
	}
	if !decision.Allowed {
		return codegen.Compiled{}, denyUnauthorized(decision)
	}
	full := andPreds(predicate, decision.RowFilter)
	scan := relalg.Scan{Relation: relalg.RelationRef{Name: target.Entity.Source, Schema: target.Entity.Schema}}
	expr := relalg.Expr(relalg.Select{Input: scan, Predicate: full})
	if len(decision.Fields) > 0 {
		items := make([]relalg.ProjectItem, len(decision.Fields))
		for i, field := range decision.Fields {
			items[i] = relalg.ProjectItem{Field: field}
		}
		expr = relalg.Project{Input: expr, Items: items}
	}
	compiled, err := codegen.Renderer{Dialect: targetTC.Dialect}.Compile(expr,
		codegen.WithPrimaryKey(target.Entity.PrimaryKey()...),
		codegen.WithIdentityKeys(target.Entity.IdentityKeys(false, tc.Transaction != "")...),
		codegen.WithMaxINCardinality(max(len(tuples), effectiveMaxIN(targetTC))))
	if err != nil {
		return codegen.Compiled{}, err
	}
	return checkGate(ctx, targetTC, compiled)
}

func appendExpandedChild(
	tc Context,
	targetTC Context,
	target entity.Resolved,
	join joinColumns,
	child map[string]any,
	grouped map[string][]map[string]any,
	returned *int64,
) error {
	(*returned)++
	if tc.BudgetLimits.MaxReturnedRows > 0 && *returned > tc.BudgetLimits.MaxReturnedRows {
		return budget.ErrExceeded
	}
	key, ok := rowJoinKey(child, join.target)
	if targetTC.Masker != nil {
		maskRow(targetTC.Masker, target.Entity.Attributes, child)
	}
	if ok {
		grouped[key] = append(grouped[key], child)
	}
	return nil
}

func attachExpandedChildren(
	rows []map[string]any,
	name string,
	join joinColumns,
	relation entity.Relationship,
	grouped map[string][]map[string]any,
) {
	for _, row := range rows {
		var children []map[string]any
		if key, ok := rowJoinKey(row, join.local); ok {
			children = grouped[key]
		}
		switch relation.Cardinality {
		case "one", "one-to-one", "belongs-to":
			if len(children) > 0 {
				row[name] = children[0]
			} else {
				row[name] = nil
			}
		default:
			if children == nil {
				children = []map[string]any{}
			}
			row[name] = children
		}
	}
}
