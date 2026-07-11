package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nethinwei/sql-mcp-server/cache"
	"github.com/nethinwei/sql-mcp-server/codegen"
	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/rbac"
	"github.com/nethinwei/sql-mcp-server/relalg"
	"github.com/nethinwei/sql-mcp-server/store"
)

// DefaultTools returns the seven DML tool instances.
func DefaultTools() []Tool {
	return []Tool{
		DescribeTool{}, ReadTool{}, CreateTool{}, UpdateTool{},
		DeleteTool{}, ExecuteTool{}, AggregateTool{},
	}
}

// ---- describe_entities ----

// DescribeTool lists entities or one entity's fields.
type DescribeTool struct{}

// Info implements Tool.
func (DescribeTool) Info() Info {
	return Info{Name: "describe_entities", Description: "List exposed entities and their fields", InputSchema: schemaDescribe, ReadOnly: true}
}

// Enabled implements Tool.
func (DescribeTool) Enabled(f config.ToolFlags) bool { return f.DescribeEntities }

// Run implements Tool.
func (DescribeTool) Run(_ context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in struct {
		Entity string `json:"entity"`
	}
	_ = json.Unmarshal(input, &in)
	if in.Entity != "" {
		res, ok := tc.Registry.Resolve(in.Entity)
		if !ok {
			return Result{}, ErrEntityNotFound
		}
		fields := make([]map[string]any, 0, len(res.Attributes))
		for _, a := range res.Attributes {
			fields = append(fields, map[string]any{
				"name": a.Name, "alias": a.Alias,
				"description": a.Description, "type": a.Domain.Type,
			})
		}
		return Result{Content: []map[string]any{{
			"name": res.Entity.Name, "description": res.Entity.Description, "fields": fields,
		}}}, nil
	}
	entities := tc.Registry.Entities()
	out := make([]map[string]any, 0, len(entities))
	for _, e := range entities {
		out = append(out, map[string]any{"name": e.Name, "description": e.Description})
	}
	return Result{Content: out}, nil
}

// ---- read_records ----

// ReadTool reads records (cost-gated).
type ReadTool struct{}

func (ReadTool) Info() Info {
	return Info{Name: "read_records", Description: "Read records from an entity", InputSchema: schemaRead, ReadOnly: true}
}
func (ReadTool) Enabled(f config.ToolFlags) bool { return f.ReadRecords }
func (ReadTool) CostGated()                      {}

func (ReadTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in readInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runRead(ctx, tc, in)
}

func runRead(ctx context.Context, tc Context, in readInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	start := time.Now()
	res, ok := tc.Registry.Resolve(in.Entity)
	if !ok {
		return Result{}, ErrEntityNotFound
	}
	pred, err := filterToPredicate(in.Filter)
	if err != nil {
		return Result{}, err
	}
	if err := validateFields(res, append(filterFields(in.Filter), in.Fields...)...); err != nil {
		return Result{}, err
	}
	dec, err := tc.Authorizer.Authorize(ctx, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionRead,
		Fields: in.Fields, Predicate: pred,
	})
	if err != nil {
		return Result{}, err
	}
	if !dec.Allowed {
		return Result{}, ErrUnauthorized
	}
	full := andPreds(pred, dec.RowFilter)
	// Keyset pagination: resume strictly after the cursor's key values with a
	// row-value comparison (composite keys need OR-expansion, not per-column >).
	var keysetCols []string
	if len(in.Cursor) > 0 {
		var ks relalg.Predicate
		ks, keysetCols = keysetAfter(res.Entity.PrimaryKey(), in.Cursor)
		full = andPreds(full, ks)
	}
	scan := relalg.Scan{Relation: relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema}}
	var input relalg.Expr = scan
	if full != nil {
		input = relalg.Select{Input: scan, Predicate: full}
	}
	if len(keysetCols) > 0 {
		order := make([]relalg.OrderTerm, len(keysetCols))
		for i, c := range keysetCols {
			order[i] = relalg.OrderTerm{Field: c, Dir: "asc"}
		}
		input = relalg.Sort{Input: input, OrderBy: order}
	}
	if len(dec.Fields) > 0 {
		items := make([]relalg.ProjectItem, len(dec.Fields))
		for i, f := range dec.Fields {
			items[i] = relalg.ProjectItem{Field: f}
		}
		input = relalg.Project{Input: input, Items: items}
	}
	if in.Limit > 0 {
		input = relalg.Limit{Input: input, Count: in.Limit, Offset: in.Offset}
	}
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(input, codegen.WithPrimaryKey(res.Entity.PrimaryKey()...))
	if err != nil {
		return Result{}, err
	}
	// Feedback keys on the pre-rewrite SQL so the Estimate layer (which runs
	// before EnforceCap wraps a LIMIT) finds the calibration record.
	planTemplate := compiled.SQL
	if tc.Gate != nil {
		gd, err := tc.Gate.Check(ctx, compiled)
		if err != nil {
			return Result{}, err
		}
		if !gd.Allow {
			return Result{}, toExceededError(gd)
		}
		if gd.Rewritten != nil {
			compiled = *gd.Rewritten
		}
	}
	key := cache.Key{Entity: in.Entity, SQL: compiled.SQL, Args: argsKey(compiled.Args)}
	if tc.Cache != nil {
		if cached, ok := tc.Cache.Get(ctx, key); ok {
			return Result{Content: cached}, nil
		}
	}
	rows, err := tc.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, err
	}
	out := make([]map[string]any, 0)
	for row, err := range store.Iter(rows) {
		if err != nil {
			return Result{}, err
		}
		if tc.Masker != nil {
			maskRow(tc.Masker, res.Entity.Attributes, row)
		}
		out = append(out, row)
	}
	if tc.Cache != nil {
		_ = tc.Cache.Set(ctx, key, out)
	}
	if tc.Feedback != nil {
		tc.Feedback.Record(cost.Feedback{Template: planTemplate, Rows: int64(len(out)), Duration: time.Since(start)})
	}
	return Result{Content: out}, nil
}

// ---- create_record ----

// CreateTool inserts one record.
type CreateTool struct{}

func (CreateTool) Info() Info {
	return Info{Name: "create_record", Description: "Create a record in an entity", InputSchema: schemaCreate}
}
func (CreateTool) Enabled(f config.ToolFlags) bool { return f.CreateRecord }
func (CreateTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in createInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runInsert(ctx, tc, in)
}

func runInsert(ctx context.Context, tc Context, in createInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	res, ok := tc.Registry.Resolve(in.Entity)
	if !ok {
		return Result{}, ErrEntityNotFound
	}
	dec, err := tc.Authorizer.Authorize(ctx, rbac.Request{Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionCreate})
	if err != nil {
		return Result{}, err
	}
	if !dec.Allowed {
		return Result{}, ErrUnauthorized
	}
	keys := sortedKeys(in.Values)
	if err := validateFields(res, keys...); err != nil {
		return Result{}, err
	}
	cols := make([]string, len(keys))
	tup := make(relalg.Tuple, len(keys))
	for i, k := range keys {
		cols[i] = k
		tup[i] = in.Values[k]
	}
	target := relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema}
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(
		relalg.Insert{Target: target, Columns: cols, Tuples: []relalg.Tuple{tup}},
		codegen.WithPrimaryKey(res.Entity.PrimaryKey()...),
	)
	if err != nil {
		return Result{}, err
	}
	// Dialects with RETURNING (PostgreSQL/OceanBase) read the generated key
	// directly; others fall back to LastInsertId.
	if tc.Dialect.Capabilities().Returning && len(res.Entity.PrimaryKey()) > 0 {
		rows, err := tc.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
		if err != nil {
			return Result{}, err
		}
		var row map[string]any
		for r, err := range store.Iter(rows) {
			if err != nil {
				return Result{}, err
			}
			row = r
			break
		}
		if tc.Cache != nil {
			_ = tc.Cache.Invalidate(in.Entity)
		}
		if row == nil {
			return Result{Content: []map[string]any{{"rowsAffected": int64(0)}}}, nil
		}
		row["rowsAffected"] = int64(1)
		return Result{Content: []map[string]any{row}}, nil
	}
	r, err := tc.DB.ExecContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, err
	}
	if tc.Cache != nil {
		_ = tc.Cache.Invalidate(in.Entity)
	}
	return Result{Content: []map[string]any{{"lastInsertId": r.LastInsertID, "rowsAffected": r.RowsAffected}}}, nil
}

// ---- update_record ----

// UpdateTool updates records (cost-gated).
type UpdateTool struct{}

func (UpdateTool) Info() Info {
	return Info{Name: "update_record", Description: "Update records in an entity", InputSchema: schemaUpdate}
}
func (UpdateTool) Enabled(f config.ToolFlags) bool { return f.UpdateRecord }
func (UpdateTool) CostGated()                      {}
func (UpdateTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in updateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runUpdate(ctx, tc, in)
}

func runUpdate(ctx context.Context, tc Context, in updateInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	res, ok := tc.Registry.Resolve(in.Entity)
	if !ok {
		return Result{}, ErrEntityNotFound
	}
	pred, err := filterToPredicate(in.Filter)
	if err != nil {
		return Result{}, err
	}
	updFields := append(filterFields(in.Filter), sortedKeys(in.Set)...)
	if err := validateFields(res, updFields...); err != nil {
		return Result{}, err
	}
	dec, err := tc.Authorizer.Authorize(ctx, rbac.Request{Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionUpdate, Predicate: pred})
	if err != nil {
		return Result{}, err
	}
	if !dec.Allowed {
		return Result{}, ErrUnauthorized
	}
	full := andPreds(pred, dec.RowFilter)
	if full == nil {
		return Result{}, ErrUnsafeWrite
	}
	setItems := make([]relalg.SetItem, 0, len(in.Set))
	for _, k := range sortedKeys(in.Set) {
		setItems = append(setItems, relalg.SetItem{Field: k, Value: in.Set[k]})
	}
	target := relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema}
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(
		relalg.Update{Target: target, Predicate: full, Set: setItems},
		codegen.WithPrimaryKey(res.Entity.PrimaryKey()...),
	)
	if err != nil {
		return Result{}, err
	}
	if tc.Gate != nil {
		gd, err := tc.Gate.Check(ctx, compiled)
		if err != nil {
			return Result{}, err
		}
		if !gd.Allow {
			return Result{}, toExceededError(gd)
		}
		if gd.Rewritten != nil {
			compiled = *gd.Rewritten
		}
	}
	r, err := tc.DB.ExecContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, err
	}
	if tc.Cache != nil {
		_ = tc.Cache.Invalidate(in.Entity)
	}
	return Result{Content: []map[string]any{{"rowsAffected": r.RowsAffected}}}, nil
}

// ---- delete_record ----

// DeleteTool deletes records (cost-gated, off by default).
type DeleteTool struct{}

func (DeleteTool) Info() Info {
	return Info{Name: "delete_record", Description: "Delete records from an entity", InputSchema: schemaDelete}
}
func (DeleteTool) Enabled(f config.ToolFlags) bool { return f.DeleteRecord }
func (DeleteTool) CostGated()                      {}
func (DeleteTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in deleteInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runDelete(ctx, tc, in)
}

func runDelete(ctx context.Context, tc Context, in deleteInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	res, ok := tc.Registry.Resolve(in.Entity)
	if !ok {
		return Result{}, ErrEntityNotFound
	}
	pred, err := filterToPredicate(in.Filter)
	if err != nil {
		return Result{}, err
	}
	if err := validateFields(res, filterFields(in.Filter)...); err != nil {
		return Result{}, err
	}
	dec, err := tc.Authorizer.Authorize(ctx, rbac.Request{Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionDelete, Predicate: pred})
	if err != nil {
		return Result{}, err
	}
	if !dec.Allowed {
		return Result{}, ErrUnauthorized
	}
	full := andPreds(pred, dec.RowFilter)
	if full == nil {
		return Result{}, ErrUnsafeWrite
	}
	target := relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema}
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(
		relalg.Delete{Target: target, Predicate: full},
		codegen.WithPrimaryKey(res.Entity.PrimaryKey()...),
	)
	if err != nil {
		return Result{}, err
	}
	if tc.Gate != nil {
		gd, err := tc.Gate.Check(ctx, compiled)
		if err != nil {
			return Result{}, err
		}
		if !gd.Allow {
			return Result{}, toExceededError(gd)
		}
		if gd.Rewritten != nil {
			compiled = *gd.Rewritten
		}
	}
	r, err := tc.DB.ExecContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, err
	}
	if tc.Cache != nil {
		_ = tc.Cache.Invalidate(in.Entity)
	}
	return Result{Content: []map[string]any{{"rowsAffected": r.RowsAffected}}}, nil
}

// ---- execute_entity (stub: stored-procedure call needs codegen extension) ----

// ExecuteTool calls a stored procedure (not yet implemented).
type ExecuteTool struct{}

func (ExecuteTool) Info() Info {
	return Info{Name: "execute_entity", Description: "Execute a stored procedure", InputSchema: schemaExecute}
}
func (ExecuteTool) Enabled(f config.ToolFlags) bool { return f.ExecuteEntity }
func (ExecuteTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in executeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runExecute(ctx, tc, in)
}

func runExecute(ctx context.Context, tc Context, in executeInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	res, ok := tc.Registry.Resolve(in.Entity)
	if !ok {
		return Result{}, ErrEntityNotFound
	}
	if res.Entity.Kind != entity.KindProcedure {
		return Result{}, fmt.Errorf("%w: %q is not a procedure", ErrInvalidInput, in.Entity)
	}
	dec, err := tc.Authorizer.Authorize(ctx, rbac.Request{Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionExecute})
	if err != nil {
		return Result{}, err
	}
	if !dec.Allowed {
		return Result{}, ErrUnauthorized
	}
	// Bind args positionally in the procedure's declared parameter order. A
	// procedure with no declared Params cannot be executed (fail-closed): the
	// map key order is not the formal-parameter order, so guessing (e.g. by
	// sorting keys) would silently write values into the wrong columns.
	args, err := orderedProcArgs(res.Entity.Params, in.Args)
	if err != nil {
		return Result{}, err
	}
	expr := relalg.Call{Procedure: relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema}, Args: args}
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(expr)
	if err != nil {
		return Result{}, err
	}
	rows, err := tc.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		// Procedure may not return a result set; fall back to Exec.
		r, eerr := tc.DB.ExecContext(ctx, compiled.SQL, compiled.Args...)
		if eerr != nil {
			return Result{}, err
		}
		return Result{Content: []map[string]any{{"rowsAffected": r.RowsAffected}}}, nil
	}
	out := make([]map[string]any, 0)
	for row, err := range store.Iter(rows) {
		if err != nil {
			return Result{}, err
		}
		if tc.Masker != nil {
			maskRow(tc.Masker, res.Entity.Attributes, row)
		}
		out = append(out, row)
	}
	return Result{Content: out}, nil
}

// orderedProcArgs binds named args to the procedure's declared parameter order.
// It rejects an undeclared procedure, unknown args, and missing args so a
// positional CALL never receives values in the wrong slots.
func orderedProcArgs(params []string, in map[string]any) ([]any, error) {
	if len(params) == 0 {
		return nil, fmt.Errorf("%w: procedure has no declared parameters; declare params to enable execute", ErrInvalidInput)
	}
	want := make(map[string]bool, len(params))
	for _, p := range params {
		want[p] = true
	}
	for k := range in {
		if !want[k] {
			return nil, fmt.Errorf("%w: unknown procedure parameter %q", ErrInvalidInput, k)
		}
	}
	args := make([]any, len(params))
	for i, p := range params {
		v, ok := in[p]
		if !ok {
			return nil, fmt.Errorf("%w: missing procedure parameter %q", ErrInvalidInput, p)
		}
		args[i] = v
	}
	return args, nil
}

// ---- aggregate_records ----

// AggregateTool runs an aggregate query (cost-gated).
type AggregateTool struct{}

func (AggregateTool) Info() Info {
	return Info{Name: "aggregate_records", Description: "Aggregate records of an entity", InputSchema: schemaAggregate, ReadOnly: true}
}
func (AggregateTool) Enabled(f config.ToolFlags) bool { return f.AggregateRecords }
func (AggregateTool) CostGated()                      {}
func (AggregateTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in aggregateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runAggregate(ctx, tc, in)
}

func runAggregate(ctx context.Context, tc Context, in aggregateInput) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	res, ok := tc.Registry.Resolve(in.Entity)
	if !ok {
		return Result{}, ErrEntityNotFound
	}
	pred, err := filterToPredicate(in.Filter)
	if err != nil {
		return Result{}, err
	}
	aggFields := filterFields(in.Filter)
	aggFields = append(aggFields, in.GroupBy...)
	for _, a := range in.Aggregates {
		if a.Field != "" {
			aggFields = append(aggFields, a.Field)
		}
	}
	if err := validateFields(res, aggFields...); err != nil {
		return Result{}, err
	}
	dec, err := tc.Authorizer.Authorize(ctx, rbac.Request{Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionAggregate, Predicate: pred})
	if err != nil {
		return Result{}, err
	}
	if !dec.Allowed {
		return Result{}, ErrUnauthorized
	}
	full := andPreds(pred, dec.RowFilter)
	scan := relalg.Scan{Relation: relalg.RelationRef{Name: res.Entity.Source, Schema: res.Entity.Schema}}
	var input relalg.Expr = scan
	if full != nil {
		input = relalg.Select{Input: scan, Predicate: full}
	}
	calls := make([]relalg.AggCall, 0, len(in.Aggregates))
	for _, a := range in.Aggregates {
		f := relalg.AggFunc(a.Func)
		if !f.Valid() {
			return Result{}, fmt.Errorf("%w: aggregate func %q", ErrInvalidInput, a.Func)
		}
		calls = append(calls, relalg.AggCall{Func: f, Field: a.Field})
	}
	input = relalg.Aggregate{Input: input, GroupBy: in.GroupBy, Aggregates: calls}
	compiled, err := codegen.Renderer{Dialect: tc.Dialect}.Compile(input, codegen.WithPrimaryKey(res.Entity.PrimaryKey()...))
	if err != nil {
		return Result{}, err
	}
	if tc.Gate != nil {
		gd, err := tc.Gate.Check(ctx, compiled)
		if err != nil {
			return Result{}, err
		}
		if !gd.Allow {
			return Result{}, toExceededError(gd)
		}
		if gd.Rewritten != nil {
			compiled = *gd.Rewritten
		}
	}
	rows, err := tc.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, err
	}
	out := make([]map[string]any, 0)
	for row, err := range store.Iter(rows) {
		if err != nil {
			return Result{}, err
		}
		out = append(out, row)
	}
	return Result{Content: out}, nil
}
