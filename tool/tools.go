package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/audit"
	"github.com/nethinwei/sql-mcp-server/budget"
	"github.com/nethinwei/sql-mcp-server/cache"
	"github.com/nethinwei/sql-mcp-server/codegen"
	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/rbac"
	"github.com/nethinwei/sql-mcp-server/relalg"
	"github.com/nethinwei/sql-mcp-server/store"
)

// DefaultTools returns the entity and explicit transaction tool instances.
func DefaultTools() []Tool {
	return []Tool{
		DescribeTool{}, ReadTool{}, CreateTool{}, UpdateTool{},
		DeleteTool{}, ExecuteTool{}, AggregateTool{},
		BeginTransactionTool{}, CommitTransactionTool{}, RollbackTransactionTool{},
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
func (DescribeTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in struct {
		Entity string `json:"entity"`
	}
	_ = json.Unmarshal(input, &in)
	if in.Entity != "" {
		res, err := resolveDMLEntity(tc, in.Entity)
		if err != nil {
			return Result{}, err
		}
		fieldNames, allowed, err := describeFields(ctx, tc, res.Entity)
		if err != nil {
			return Result{}, err
		}
		if !allowed {
			return Result{}, ErrUnauthorized
		}
		fields := make([]map[string]any, 0, len(fieldNames))
		for _, name := range fieldNames {
			a, ok := res.Entity.AttributeByName(name)
			if !ok || a.Excluded {
				continue
			}
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
		if !e.MCP.DMLTools {
			continue
		}
		_, allowed, err := describeFields(ctx, tc, e)
		if err != nil {
			return Result{}, err
		}
		if !allowed {
			continue
		}
		out = append(out, map[string]any{"name": e.Name, "description": e.Description})
	}
	return Result{Content: out}, nil
}

func describeFields(ctx context.Context, tc Context, e entity.Entity) ([]string, bool, error) {
	actions := []entity.Action{
		entity.ActionRead, entity.ActionCreate, entity.ActionUpdate,
		entity.ActionDelete, entity.ActionAggregate,
	}
	if e.Kind == entity.KindProcedure {
		actions = []entity.Action{entity.ActionExecute}
	}
	allowedEntity := false
	fields := make(map[string]bool)
	for _, action := range actions {
		dec, err := authorize(ctx, tc, rbac.Request{
			Role: tc.Role, Subject: tc.Subject, Entity: e.Name, Action: action,
		})
		if err != nil {
			return nil, false, err
		}
		if !dec.Allowed {
			continue
		}
		allowedEntity = true
		for _, attr := range e.Attributes {
			if attr.Excluded {
				continue
			}
			requests := []rbac.Request{{
				Role: tc.Role, Subject: tc.Subject, Entity: e.Name, Action: action, Fields: []string{attr.Name},
			}}
			switch action {
			case entity.ActionCreate:
				requests[0].Fields = nil
				requests[0].WriteFields = []string{attr.Name}
			case entity.ActionUpdate:
				requests[0].Fields = nil
				requests[0].ReadFields = []string{attr.Name}
				requests = append(requests, rbac.Request{
					Role: tc.Role, Subject: tc.Subject, Entity: e.Name, Action: action,
					WriteFields: []string{attr.Name},
				})
			case entity.ActionDelete:
				requests[0].Fields = nil
				requests[0].ReadFields = []string{attr.Name}
			}
			for _, request := range requests {
				fieldDecision, err := authorize(ctx, tc, request)
				if err != nil {
					return nil, false, err
				}
				if fieldDecision.Allowed {
					fields[attr.Name] = true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(fields))
	for _, attr := range e.Attributes {
		if fields[attr.Name] {
			out = append(out, attr.Name)
		}
	}
	return out, allowedEntity, nil
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
	tc.Transaction = in.Transaction
	start := time.Now()
	res, err := resolveDMLEntity(tc, in.Entity)
	if err != nil {
		return Result{}, err
	}
	tc, err = routeEntity(tc, res.Entity)
	if err != nil {
		return Result{}, err
	}
	pred, err := filterToPredicate(in.Filter)
	if err != nil {
		return Result{}, err
	}
	readFields := append(filterFields(in.Filter), in.Fields...)
	projectionFields := append([]string(nil), in.Fields...)
	for _, name := range in.Expand {
		relation, ok := relationshipByName(res.Entity, name)
		if !ok {
			return Result{}, fmt.Errorf("%w: unknown relationship %q", ErrInvalidInput, name)
		}
		for local := range relation.JoinOn {
			readFields = append(readFields, local)
			if len(in.Fields) > 0 && !containsString(projectionFields, local) {
				projectionFields = append(projectionFields, local)
			}
		}
	}
	readFields = append(readFields, sortedKeys(in.Cursor)...)
	if err := validateFields(res, readFields...); err != nil {
		return Result{}, err
	}
	dec, err := authorize(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionRead,
		Fields: projectionFields, ReadFields: readFields, Predicate: pred,
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
	planTemplate := cost.Fingerprint(tc.DataSource, tc.Dialect.Name(), compiled)
	var estimatedPlan *cost.Plan
	compiled, estimatedPlan, err = checkGateDetailed(ctx, tc, compiled)
	if err != nil {
		return Result{}, err
	}
	key := cache.Key{
		Entity: in.Entity, SQL: compiled.SQL + "\x00expand=" + strings.Join(in.Expand, ","), Args: argsKey(compiled.Args),
		Scope: scopeKey(tc.Role, tc.Subject),
	}
	if tc.Cache != nil && in.Transaction == "" && len(in.Expand) == 0 {
		if cached, ok := tc.Cache.Get(ctx, key); ok {
			returned := countReadRows(cached, in.Expand)
			if tc.BudgetLimits.MaxReturnedRows > 0 && returned > tc.BudgetLimits.MaxReturnedRows {
				return Result{}, budget.ErrExceeded
			}
			return Result{Content: cached, ReturnedRows: returned}, nil
		}
	}
	rows, err := tc.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, err
	}
	out := make([]map[string]any, 0)
	var returned int64
	for row, err := range store.Iter(rows) {
		if err != nil {
			return Result{}, err
		}
		returned++
		if tc.BudgetLimits.MaxReturnedRows > 0 && returned > tc.BudgetLimits.MaxReturnedRows {
			return Result{}, budget.ErrExceeded
		}
		out = append(out, row)
	}
	if len(in.Expand) > 0 {
		if err := expandRows(ctx, tc, res.Entity, out, in.Expand, &returned); err != nil {
			return Result{}, err
		}
	}
	if len(in.Fields) > 0 {
		for _, row := range out {
			for _, name := range in.Expand {
				relation, _ := relationshipByName(res.Entity, name)
				for local := range relation.JoinOn {
					if !containsString(in.Fields, local) {
						delete(row, local)
					}
				}
			}
		}
	}
	if tc.Masker != nil {
		for _, row := range out {
			maskRow(tc.Masker, res.Entity.Attributes, row)
		}
	}
	recordReadFeedback(ctx, tc, in.Entity, compiled, planTemplate, estimatedPlan, int64(len(out)), time.Since(start))
	if tc.Cache != nil && in.Transaction == "" && len(in.Expand) == 0 {
		_ = tc.Cache.Set(ctx, key, out)
	}
	return Result{Content: out, ReturnedRows: returned}, nil
}

func recordReadFeedback(
	ctx context.Context,
	tc Context,
	entityName string,
	compiled codegen.Compiled,
	template string,
	estimatedPlan *cost.Plan,
	fallbackRows int64,
	fallbackDuration time.Duration,
) {
	if tc.Feedback == nil {
		return
	}
	estimatedRows := int64(0)
	if estimatedPlan != nil {
		estimatedRows = estimatedPlan.EstimatedRows
	}
	actualRows, duration := fallbackRows, fallbackDuration
	if tc.Transaction == "" {
		if plan, sampled, err := tc.Analyze.Sample(ctx, compiled); err != nil {
			samplingErr := fmt.Errorf("EXPLAIN ANALYZE sampling failed: %w", err)
			tc.Hooks.FireOnError(ctx, samplingErr)
			if tc.Auditor != nil {
				_ = tc.Auditor.Record(ctx, audit.Event{
					Time: time.Now(), Entity: entityName, Action: "explain_analyze_sample",
					Tool: "read_records", Allowed: false, Error: samplingErr.Error(),
				})
			}
		} else if sampled {
			actualRows, duration = plan.ActualRows, plan.ExecutionTime
		}
	}
	tc.Feedback.Record(cost.Feedback{
		Template: template, EstimatedRows: estimatedRows,
		ActualRows: actualRows, Duration: duration,
	})
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
	tc.Transaction = in.Transaction
	res, err := resolveDMLEntity(tc, in.Entity)
	if err != nil {
		return Result{}, err
	}
	tc, err = routeEntity(tc, res.Entity)
	if err != nil {
		return Result{}, err
	}
	keys := sortedKeys(in.Values)
	if err := validateFields(res, keys...); err != nil {
		return Result{}, err
	}
	dec, err := authorize(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionCreate,
		WriteFields: keys,
	})
	if err != nil {
		return Result{}, err
	}
	if !dec.Allowed {
		return Result{}, ErrUnauthorized
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
		if err := afterWrite(tc, res.Entity, in.Transaction); err != nil {
			return Result{}, err
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
	if err := afterWrite(tc, res.Entity, in.Transaction); err != nil {
		return Result{}, err
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
	tc.Transaction = in.Transaction
	res, err := resolveDMLEntity(tc, in.Entity)
	if err != nil {
		return Result{}, err
	}
	tc, err = routeEntity(tc, res.Entity)
	if err != nil {
		return Result{}, err
	}
	pred, err := filterToPredicate(in.Filter)
	if err != nil {
		return Result{}, err
	}
	updFields := append(filterFields(in.Filter), sortedKeys(in.Set)...)
	if err := validateFields(res, updFields...); err != nil {
		return Result{}, err
	}
	dec, err := authorize(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionUpdate,
		ReadFields: filterFields(in.Filter), WriteFields: sortedKeys(in.Set), Predicate: pred,
	})
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
	compiled, err = checkGate(ctx, tc, compiled)
	if err != nil {
		return Result{}, err
	}
	r, err := tc.DB.ExecContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, err
	}
	if err := afterWrite(tc, res.Entity, in.Transaction); err != nil {
		return Result{}, err
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
	tc.Transaction = in.Transaction
	res, err := resolveDMLEntity(tc, in.Entity)
	if err != nil {
		return Result{}, err
	}
	tc, err = routeEntity(tc, res.Entity)
	if err != nil {
		return Result{}, err
	}
	pred, err := filterToPredicate(in.Filter)
	if err != nil {
		return Result{}, err
	}
	if err := validateFields(res, filterFields(in.Filter)...); err != nil {
		return Result{}, err
	}
	dec, err := authorize(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionDelete,
		ReadFields: filterFields(in.Filter), Predicate: pred,
	})
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
	compiled, err = checkGate(ctx, tc, compiled)
	if err != nil {
		return Result{}, err
	}
	r, err := tc.DB.ExecContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, err
	}
	if err := afterWrite(tc, res.Entity, in.Transaction); err != nil {
		return Result{}, err
	}
	return Result{Content: []map[string]any{{"rowsAffected": r.RowsAffected}}}, nil
}

// ---- execute_entity ----

// ExecuteTool calls a stored procedure.
type ExecuteTool struct{}

func (ExecuteTool) Info() Info {
	return Info{Name: "execute_entity", Description: "Execute a stored procedure", InputSchema: schemaExecute}
}
func (ExecuteTool) Enabled(f config.ToolFlags) bool { return f.ExecuteEntity }
func (ExecuteTool) CostGated()                      {}
func (ExecuteTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in executeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runExecute(ctx, tc, in, true)
}

func runExecute(ctx context.Context, tc Context, in executeInput, requireDMLTools bool) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc)
	defer cancel()
	tc.Transaction = in.Transaction
	res, ok := tc.Registry.Resolve(in.Entity)
	if !ok {
		return Result{}, ErrEntityNotFound
	}
	if requireDMLTools && !res.Entity.MCP.DMLTools {
		return Result{}, ErrDMLToolsDisabled
	}
	if res.Entity.Kind != entity.KindProcedure {
		return Result{}, fmt.Errorf("%w: %q is not a procedure", ErrInvalidInput, in.Entity)
	}
	routed, err := routeEntity(tc, res.Entity)
	if err != nil {
		return Result{}, err
	}
	tc = routed
	dec, err := authorize(ctx, tc, rbac.Request{Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionExecute})
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
	compiled, err = checkGate(ctx, tc, compiled)
	if err != nil {
		return Result{}, err
	}
	rows, err := tc.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
	if err != nil {
		return Result{}, err
	}
	if err := afterWrite(tc, res.Entity, in.Transaction); err != nil {
		return Result{}, err
	}
	allowedFields := make(map[string]bool, len(dec.Fields))
	for _, field := range dec.Fields {
		if attr, ok := res.Entity.AttributeByName(field); ok && !attr.Excluded {
			allowedFields[attr.Name] = true
			if attr.Alias != "" {
				allowedFields[attr.Alias] = true
			}
		}
	}
	out := make([]map[string]any, 0)
	for row, err := range store.Iter(rows) {
		if err != nil {
			return Result{}, err
		}
		for field := range row {
			if !allowedFields[field] {
				delete(row, field)
			}
		}
		if tc.Masker != nil {
			maskRow(tc.Masker, res.Attributes, row)
		}
		if tc.BudgetLimits.MaxReturnedRows > 0 && int64(len(out)+1) > tc.BudgetLimits.MaxReturnedRows {
			return Result{}, budget.ErrExceeded
		}
		out = append(out, row)
	}
	return Result{Content: out}, nil
}

// ProcedureTool exposes one configured procedure as an independent MCP tool.
// It accepts the procedure parameters directly and delegates execution to the
// same implementation as execute_entity.
type ProcedureTool struct {
	Entity entity.Entity
}

// ProcedureToolName returns a stable MCP-safe name. The prefix separates
// procedure tools from built-ins; the hash prevents normalization collisions.
func ProcedureToolName(entityName string) string {
	var normalized strings.Builder
	for _, r := range entityName {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			normalized.WriteRune(r)
		default:
			normalized.WriteByte('_')
		}
	}
	base := normalized.String()
	if base == "" {
		base = "procedure"
	}
	sum := sha256.Sum256([]byte(entityName))
	return "procedure_" + base + "_" + hex.EncodeToString(sum[:4])
}

// Info implements Tool.
func (t ProcedureTool) Info() Info {
	properties := make(map[string]any, len(t.Entity.Params))
	for _, param := range t.Entity.Params {
		properties[param] = map[string]any{"description": "Stored procedure parameter"}
	}
	schema, _ := json.Marshal(map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   t.Entity.Params,
	})
	return Info{
		Name:        ProcedureToolName(t.Entity.Name),
		Description: "Execute stored procedure " + t.Entity.Name,
		InputSchema: schema,
	}
}

// Enabled implements Tool. Procedure tools are registered independently of the
// generic tool flags.
func (ProcedureTool) Enabled(config.ToolFlags) bool { return true }
func (ProcedureTool) CostGated()                    {}

// Run implements Tool.
func (t ProcedureTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var args map[string]any
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return runExecute(ctx, tc, executeInput{Entity: t.Entity.Name, Args: args}, false)
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
	tc.Transaction = in.Transaction
	res, err := resolveDMLEntity(tc, in.Entity)
	if err != nil {
		return Result{}, err
	}
	tc, err = routeEntity(tc, res.Entity)
	if err != nil {
		return Result{}, err
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
	dec, err := authorize(ctx, tc, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionAggregate,
		ReadFields: aggFields, Predicate: pred,
	})
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
	compiled, err = checkGate(ctx, tc, compiled)
	if err != nil {
		return Result{}, err
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
		if tc.BudgetLimits.MaxReturnedRows > 0 && int64(len(out)+1) > tc.BudgetLimits.MaxReturnedRows {
			return Result{}, budget.ErrExceeded
		}
		out = append(out, row)
	}
	return Result{Content: out}, nil
}

func relationshipByName(e entity.Entity, name string) (entity.Relationship, bool) {
	for _, relation := range e.Relations {
		if relation.Name == name {
			return relation, true
		}
	}
	return entity.Relationship{}, false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// expandRows executes one batched IN query per requested relationship. Nested
// expansion and cross-data-source joins are intentionally unsupported.
func expandRows(ctx context.Context, tc Context, parent entity.Entity, rows []map[string]any, names []string, returned *int64) error {
	for _, name := range names {
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
		if target.Entity.DataSource != parent.DataSource {
			return fmt.Errorf("%w: cross-datasource relationship %q", ErrInvalidInput, name)
		}
		targetTC, err := routeEntity(tc, target.Entity)
		if err != nil {
			return err
		}
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
		if len(values) == 0 {
			for _, row := range rows {
				row[name] = []map[string]any{}
			}
			continue
		}
		predicate := relalg.Condition{Field: targetField, Op: relalg.OpIn, Value: values}
		decision, err := authorize(ctx, targetTC, rbac.Request{
			Role: tc.Role, Subject: tc.Subject, Entity: relation.Target,
			Action: entity.ActionRead, ReadFields: []string{targetField}, Predicate: predicate,
		})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return ErrUnauthorized
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
		compiled, err := codegen.Renderer{Dialect: targetTC.Dialect}.Compile(expr, codegen.WithPrimaryKey(target.Entity.PrimaryKey()...))
		if err != nil {
			return err
		}
		compiled, err = checkGate(ctx, targetTC, compiled)
		if err != nil {
			return err
		}
		resultRows, err := targetTC.DB.QueryContext(ctx, compiled.SQL, compiled.Args...)
		if err != nil {
			return err
		}
		grouped := map[string][]map[string]any{}
		for child, iterErr := range store.Iter(resultRows) {
			if iterErr != nil {
				return iterErr
			}
			(*returned)++
			if tc.BudgetLimits.MaxReturnedRows > 0 && *returned > tc.BudgetLimits.MaxReturnedRows {
				return budget.ErrExceeded
			}
			key := fmt.Sprintf("%T:%v", child[targetField], child[targetField])
			if targetTC.Masker != nil {
				maskRow(targetTC.Masker, target.Entity.Attributes, child)
			}
			grouped[key] = append(grouped[key], child)
		}
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
	return nil
}

func countReadRows(rows []map[string]any, expand []string) int64 {
	count := int64(len(rows))
	for _, row := range rows {
		for _, name := range expand {
			switch children := row[name].(type) {
			case []map[string]any:
				count += int64(len(children))
			case map[string]any:
				count++
			}
		}
	}
	return count
}
