package tool

import (
	"context"
	"encoding/json"
	"fmt"
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
	dec              rbac.Decision
}

func runRead(ctx context.Context, tc Context, in readInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
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
	tc, err = routeEntity(tc, res.Entity)
	if err != nil {
		return readPlan{}, err
	}
	pred, err := filterToPredicate(in.Filter)
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
		return readPlan{}, ErrUnauthorized
	}
	full := andPreds(pred, dec.RowFilter)
	if len(in.Cursor) > 0 {
		ks, _ := keysetAfter(res.Entity.PrimaryKey(), in.Cursor)
		full = andPreds(full, ks)
	}
	return readPlan{res: res, tc: tc, projectionFields: projectionFields, full: full, dec: dec}, nil
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
			if len(in.Fields) > 0 && !containsString(projectionFields, local) {
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
		Entity: in.Entity, SQL: compiled.SQL + "\x00expand=" + strings.Join(in.Expand, ","),
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
	if len(in.Cursor) > 0 {
		_, keysetCols := keysetAfter(res.Entity.PrimaryKey(), in.Cursor)
		if len(keysetCols) > 0 {
			order := make([]relalg.OrderTerm, len(keysetCols))
			for i, c := range keysetCols {
				order[i] = relalg.OrderTerm{Field: c, Dir: "asc"}
			}
			expr = relalg.Sort{Input: expr, OrderBy: order}
		}
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
	if tc.Cache == nil || in.Transaction != "" || len(in.Expand) > 0 {
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
				if !containsString(in.Fields, local) {
					delete(row, local)
				}
			}
		}
	}
}

func storeReadCache(ctx context.Context, tc Context, in readInput, key cache.Key, out []map[string]any) {
	if tc.Cache == nil || in.Transaction != "" || len(in.Expand) > 0 {
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
	if len(relation.JoinOn) != 1 {
		return fmt.Errorf("%w: relationship %q requires exactly one join pair", ErrInvalidInput, name)
	}
	var localField, targetField string
	for localField, targetField = range relation.JoinOn {
	}
	target, err := resolveDMLEntity(tc, relation.Target)
	if err != nil {
		return err
	}
	if err := validateFields(target, targetField); err != nil {
		return err
	}
	if target.Entity.DataSource != parent.DataSource {
		return fmt.Errorf("%w: cross-datasource relationship %q", ErrInvalidInput, name)
	}
	targetTC, err := routeEntity(tc, target.Entity)
	if err != nil {
		return err
	}
	values := uniqueJoinValues(rows, localField)
	if len(values) == 0 {
		for _, row := range rows {
			row[name] = []map[string]any{}
		}
		return nil
	}
	grouped, err := queryExpandedChildren(ctx, tc, targetTC, target, relation, targetField, values, returned)
	if err != nil {
		return err
	}
	attachExpandedChildren(rows, name, localField, relation, grouped)
	return nil
}

func uniqueJoinValues(rows []map[string]any, localField string) []any {
	values := make([]any, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		value, ok := row[localField]
		if !ok {
			continue
		}
		key := fmt.Sprintf("%T:%v", value, value)
		if !seen[key] {
			seen[key] = true
			values = append(values, value)
		}
	}
	return values
}

func queryExpandedChildren(
	ctx context.Context,
	tc Context,
	targetTC Context,
	target entity.Resolved,
	relation entity.Relationship,
	targetField string,
	values []any,
	returned *int64,
) (map[string][]map[string]any, error) {
	grouped := map[string][]map[string]any{}
	batchSize := effectiveMaxIN(targetTC)
	for start := 0; start < len(values); start += batchSize {
		stop := min(start+batchSize, len(values))
		batch, err := queryExpandBatch(
			ctx, tc, targetTC, target, relation, targetField, values[start:stop], batchSize, returned,
		)
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
	targetField string,
	values []any,
	batchSize int,
	returned *int64,
) (map[string][]map[string]any, error) {
	compiled, err := compileExpandBatch(ctx, tc, targetTC, target, relation, targetField, values, batchSize)
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
		if err := appendExpandedChild(tc, targetTC, target, targetField, child, grouped, returned); err != nil {
			return nil, err
		}
	}
	return grouped, nil
}

func compileExpandBatch(
	ctx context.Context,
	tc Context,
	targetTC Context,
	target entity.Resolved,
	relation entity.Relationship,
	targetField string,
	values []any,
	batchSize int,
) (codegen.Compiled, error) {
	predicate := relalg.Condition{Field: targetField, Op: relalg.OpIn, Value: values}
	decision, err := authorize(ctx, targetTC, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: relation.Target,
		Action: entity.ActionRead, ReadFields: []string{targetField}, Predicate: predicate,
	})
	if err != nil {
		return codegen.Compiled{}, err
	}
	if !decision.Allowed {
		return codegen.Compiled{}, ErrUnauthorized
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
		codegen.WithMaxINCardinality(batchSize))
	if err != nil {
		return codegen.Compiled{}, err
	}
	return checkGate(ctx, targetTC, compiled)
}

func appendExpandedChild(
	tc Context,
	targetTC Context,
	target entity.Resolved,
	targetField string,
	child map[string]any,
	grouped map[string][]map[string]any,
	returned *int64,
) error {
	(*returned)++
	if tc.BudgetLimits.MaxReturnedRows > 0 && *returned > tc.BudgetLimits.MaxReturnedRows {
		return budget.ErrExceeded
	}
	key := fmt.Sprintf("%T:%v", child[targetField], child[targetField])
	if targetTC.Masker != nil {
		maskRow(targetTC.Masker, target.Entity.Attributes, child)
	}
	grouped[key] = append(grouped[key], child)
	return nil
}

func attachExpandedChildren(
	rows []map[string]any,
	name, localField string,
	relation entity.Relationship,
	grouped map[string][]map[string]any,
) {
	for _, row := range rows {
		value := row[localField]
		children := grouped[fmt.Sprintf("%T:%v", value, value)]
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
