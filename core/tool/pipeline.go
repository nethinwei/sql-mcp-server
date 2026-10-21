package tool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

// The data plane runs every database statement through one executor: a tool
// plans what to run, and a statement is the plan. A statement is made only
// from an authorization (grant, made only by authorizeRequest) and SQL the
// cost gate let through (gated, made only by gateSQL, or by admitInsert for
// the single-row INSERT of the create tool, which is not cost-gated); the
// executor then
// shapes and masks every row, counts it against the call's limits as it is
// read, classifies the database's errors and follows every write. No tool
// calls the database itself (TestOnlyTheExecutorCallsTheDatabase).

// grant is a request's authorization.
type grant struct{ rbac.Decision }

// authorizeRequest authorizes req, refusing it unless allowed, then refuses
// an entity the database cannot serve (see entity.Entity.Unavailable): only
// after authorization, so that a caller without access cannot tell it apart
// from any other entity it may not use (TM-002).
func authorizeRequest(ctx context.Context, tc Context, req rbac.Request) (grant, error) {
	dec, err := authorize(ctx, tc, req)
	if err != nil {
		return grant{}, err
	}
	if !dec.Allowed {
		return grant{}, denyUnauthorized(dec)
	}
	if req.Action == entity.ActionRead && len(dec.Fields) == 0 {
		// A read selects the fields it may return: none would be SELECT *,
		// every column, hidden ones too. The authorizer refuses it; an
		// authorizer that does not is refused here (fail closed).
		return grant{}, denyUnauthorized(rbac.Decision{Reason: "no readable fields"})
	}
	if tc.Registry != nil {
		if res, ok := tc.Registry.Resolve(req.Entity); ok && res.Entity.Unavailable != "" {
			return grant{}, fmt.Errorf("%w: %q: %s", ErrEntityUnavailable, req.Entity, res.Entity.Unavailable)
		}
	}
	return grant{dec}, nil
}

// gated is SQL the cost gate let through, rewritten as the gate asked, with
// the plan it estimated (nil without an estimate).
type gated struct {
	codegen.Compiled
	plan *cost.Plan
}

// gateSQL passes compiled through tc's cost gate.
func gateSQL(ctx context.Context, tc Context, compiled codegen.Compiled) (gated, error) {
	checked, plan, err := checkGateDetailed(ctx, tc, compiled)
	if err != nil {
		return gated{}, err
	}
	return gated{Compiled: checked, plan: plan}, nil
}

// admitInsert admits a single-row INSERT, which has no cost to bound: the
// create tool is not cost-gated (it is no CostGated tool).
func admitInsert(compiled codegen.Compiled) gated {
	return gated{Compiled: compiled}
}

// authorizeSQL authorizes the SQL escape hatch (execute_sql) on datasource
// for tc's caller: the datasource must admit the hatch (tc.Escapes holds the
// admitting datasources only, and none while tools.executeSql is off) and
// the caller must hold sql:execute@<datasource>, which only a configured
// user can. Which condition failed reaches the audit log alone; the client
// sees one unauthorized.
func authorizeSQL(tc Context, datasource string) (grant, EscapeRoute, error) {
	permission := config.SQLExecutePermission(datasource)
	route, admitted := tc.Escapes[datasource]
	if !admitted {
		return grant{}, EscapeRoute{}, fmt.Errorf("%w: datasource %q does not admit execute_sql",
			ErrUnauthorized, datasource)
	}
	checker, ok := tc.Authorizer.(rbac.PermissionChecker)
	if !ok || !checker.HasPermission(tc.Role, permission) {
		return grant{}, EscapeRoute{}, fmt.Errorf("%w: %s does not hold %s", ErrUnauthorized, tc.Role, permission)
	}
	return grant{rbac.Decision{Allowed: true, Grants: []string{"permission:" + permission}}}, route, nil
}

// estimateSQL is the escape hatch's cost gate: EXPLAIN on the escape
// connection, judged as the Estimate layer judges plans, and a statement
// EXPLAIN cannot estimate refused. There is no estimate when the
// configuration skips it; a route without EXPLAIN while the estimate is
// required refuses every statement (assembly refuses that configuration, so
// this only backs it up). The hard limits apply either way.
func estimateSQL(ctx context.Context, tc Context, route EscapeRoute, sql string, args []any, query bool) (
	gated, error,
) {
	compiled := codegen.Compiled{SQL: sql, Args: args}
	if !tc.SQLEscapeEstimateRequired || query && tc.SQLEscapeMetadata && isMetadataStatement(sql) {
		return gated{Compiled: compiled}, nil
	}
	if route.Explainer == nil {
		return gated{}, &UnestimableError{Cause: errNoExplain}
	}
	plan, err := route.Explainer.Explain(ctx, sql, args)
	if err != nil {
		tc.Hooks.FireCostGate(ctx, cost.Plan{ScanType: cost.ScanUnknown}, cost.Score{}, "hard")
		return gated{}, &UnestimableError{Cause: err}
	}
	dec := cost.Estimate{Threshold: route.Threshold.Tighten(tc.Tightening), FailClosed: true}.Evaluate(plan, nil)
	if err := admitDecision(ctx, tc, dec); err != nil {
		return gated{}, err
	}
	return gated{Compiled: compiled, plan: dec.Plan}, nil
}

// admitDecision reports the gate's decision (FireCostGate) and admits what
// it allowed within the caller's scanned-row budget: the one way the
// governed tools and the escape hatch judge a plan.
func admitDecision(ctx context.Context, tc Context, dec cost.Decision) error {
	verdict := "allow"
	switch {
	case dec.Allow:
	case dec.Soft:
		verdict = "soft"
	default:
		verdict = "hard"
	}
	estimated, score := dec.Estimate()
	tc.Hooks.FireCostGate(ctx, estimated, score, verdict)
	if !dec.Allow {
		return toExceededError(dec)
	}
	return checkScannedBudget(tc, dec.Plan)
}

// checkScannedBudget holds a plan the cost gate allowed to the caller's
// scanned-row budget. It comes after the gate's verdict: a plan the gate
// refuses is refused as too costly, which tells how to narrow it, where
// waiting for the budget to reset never helps.
func checkScannedBudget(tc Context, plan *cost.Plan) error {
	limit := tc.BudgetLimits.MaxEstimatedScannedRows
	if plan == nil || !plan.OverScanLimit(limit) {
		return nil
	}
	if rows, known := plan.ScannedRowsKnown(); known {
		return fmt.Errorf("%w: estimated scanned rows %d exceed limit %d", budget.ErrExceeded, rows, limit)
	}
	return fmt.Errorf("%w: a full scan of a table of unknown size may exceed the scanned-row limit %d",
		budget.ErrExceeded, limit)
}

// estimatedRows is the rows the gate estimated the statement scans.
func (g gated) estimatedRows() int64 {
	if g.plan == nil {
		return 0
	}
	return g.plan.ScannedRows()
}

// statement is one database statement, ready to run.
type statement struct {
	tc    Context         // routed to the entity's datasource and connection
	res   entity.Resolved // the entity whose rows it reads or writes
	grant grant
	sql   gated
	// fields, when set, are the only fields a row keeps (a procedure's
	// result, which no projection narrows).
	fields map[string]bool
	// keep are columns whose values are kept before the rows are masked, to
	// join on them.
	keep []string
	// columns, when set, name its result columns by position, whatever the
	// database names them (an aggregate's).
	columns []string
	// rowCap bounds the rows of the statement (0: no bound of its own).
	rowCap int64
	write  bool // a write: its errors and its effects are followed
	// raw is the escape hatch's statement, of no entity: its rows are
	// neither shaped nor masked and are cut at rowCap rather than refused,
	// its errors keep the driver's message, and whatever its mode it may
	// have changed anything in its database.
	raw bool
}

// newStatement plans running sql on tc for res under g.
func newStatement(tc Context, res entity.Resolved, g grant, sql gated) statement {
	return statement{tc: tc, res: res, grant: g, sql: sql}
}

// newRawStatement plans running the escape hatch's sql on tc under g: a
// write when exec, bounded by the stricter of the call's row caps.
func newRawStatement(tc Context, g grant, sql gated, exec bool) statement {
	return statement{
		tc: tc, grant: g, sql: sql, raw: true, write: exec,
		rowCap: minLimit(tc.MaxRows, tc.BudgetLimits.MaxReturnedRows),
	}
}

// rawRows are an escape hatch query's rows: positional under their column
// names (a statement may return one name twice), cut at the row cap.
type rawRows struct {
	columns   []string
	rows      [][]any
	truncated bool
}

// queryRaw runs st, the escape hatch's query, and reads its rows as they
// are, counted against the call's byte limit and cut at its row cap.
func (st statement) queryRaw(ctx context.Context) (out rawRows, err error) {
	if err := st.admitted(ctx); err != nil {
		return rawRows{}, err
	}
	defer func() { err = st.settle(ctx, err) }()
	rows, err := retryStalePlan(st.tc, func() (store.Rows, error) {
		return st.tc.DB.QueryContext(ctx, st.sql.SQL, st.sql.Args...)
	})
	if err != nil {
		return rawRows{}, st.fail(err)
	}
	defer func() { _ = rows.Close() }()
	if out.columns, err = rows.Columns(); err != nil {
		return rawRows{}, st.fail(err)
	}
	out.rows = [][]any{}
	for rows.Next() {
		if st.rowCap > 0 && int64(len(out.rows)) >= st.rowCap {
			out.truncated = true
			break
		}
		values := make([]any, len(out.columns))
		pointers := make([]any, len(out.columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return rawRows{}, st.fail(err)
		}
		if err := st.tc.results.addValues(values); errors.Is(err, errResultCut) {
			out.truncated = true
			break
		} else if err != nil {
			return rawRows{}, err
		}
		out.rows = append(out.rows, values)
	}
	if err := rows.Err(); err != nil {
		return rawRows{}, st.fail(err)
	}
	return out, nil
}

// admitted refuses a statement made without an authorization or without SQL
// (the zero grant or gated a failed step returns). A write inside a
// transaction records what it may change before it runs: a commit coming
// between its end and its settling would otherwise leave the cached reads
// it changed in place (a write that fails rolls the transaction back, and
// what it recorded with it).
func (st statement) admitted(ctx context.Context) error {
	if !st.grant.Allowed || st.sql.SQL == "" {
		return ErrUnauthorized
	}
	if st.transactional() {
		return afterWrite(ctx, st.tc, st.res.Entity, st.tc.Transaction)
	}
	return nil
}

// transactional reports a governed write inside a transaction.
func (st statement) transactional() bool {
	return st.write && !st.raw && st.tc.Transaction != ""
}

// queried are a query's rows and, for each, the values of the statement's
// kept columns as read.
type queried struct {
	rows []map[string]any
	kept []map[string]any
	// truncated: a write's rows were cut short past the result memory.
	truncated bool
}

// query runs st and reads its rows: shaped, masked and counted against the
// call's limits, stopping at the first that crosses them.
func (st statement) query(ctx context.Context) (out queried, err error) {
	if err := st.admitted(ctx); err != nil {
		return queried{}, err
	}
	defer func() { err = st.settle(ctx, err) }()
	rows, err := retryStalePlan(st.tc, func() (store.Rows, error) {
		return st.tc.DB.QueryContext(ctx, st.sql.SQL, st.sql.Args...)
	})
	if err != nil {
		return queried{}, st.fail(err)
	}
	out.rows = make([]map[string]any, 0)
	rules := st.maskRules()
	for row, err := range store.IterNamed(rows, st.columns) {
		if err != nil {
			return queried{}, st.fail(err)
		}
		if st.keep != nil {
			kept := make(map[string]any, len(st.keep))
			for _, c := range st.keep {
				kept[c] = row[c]
			}
			out.kept = append(out.kept, kept)
		}
		st.present(row, rules)
		err := errResultCut // a write's rows past its cap are cut: it already took effect
		if st.rowCap <= 0 || int64(len(out.rows)) < st.rowCap {
			err = st.tc.results.add(row)
		} else if !st.write {
			return queried{}, budget.ErrExceeded
		}
		if errors.Is(err, errResultCut) {
			out.truncated = true
			if st.keep != nil {
				out.kept = out.kept[:len(out.rows)]
			}
			return out, nil
		} else if err != nil {
			return queried{}, err
		}
		out.rows = append(out.rows, row)
	}
	return out, nil
}

// exec runs st, a statement without rows.
func (st statement) exec(ctx context.Context) (store.Result, error) {
	if err := st.admitted(ctx); err != nil {
		return store.Result{}, err
	}
	r, err := retryStalePlan(st.tc, func() (store.Result, error) {
		return st.tc.DB.ExecContext(ctx, st.sql.SQL, st.sql.Args...)
	})
	if err != nil {
		err = st.fail(err)
	}
	return r, st.settle(ctx, err)
}

// maskRules are the mask rules of st's rows (see present); nil without a
// masker.
func (st statement) maskRules() map[string]string {
	if st.tc.Masker == nil {
		return nil
	}
	return maskRules(st.res.Entity.Attributes)
}

// present shapes a row of st's entity for the caller and masks it by rules
// (st.maskRules, read once for the rows of a statement).
func (st statement) present(row map[string]any, rules map[string]string) {
	if st.fields != nil {
		for field := range row {
			if !st.fields[field] {
				delete(row, field)
			}
		}
	}
	if st.tc.Masker != nil {
		maskRow(st.tc.Masker, rules, row)
	}
}

// fail classifies a database error of st. A statement failing inside an
// explicit transaction ended it (see TransactionManager), which its error
// tells.
func (st statement) fail(err error) error {
	out := st.classify(err)
	if endedTransaction(err) {
		return &TransactionRolledBackError{Err: out}
	}
	return out
}

func (st statement) classify(err error) error {
	if st.raw {
		return st.failRaw(err)
	}
	if changed := st.schemaChange(err); changed != nil {
		return changed
	}
	if !st.write {
		return WrapDBError(err)
	}
	if st.tc.Transaction == "" && uncertain(err) {
		// Inside a transaction nothing is committed yet; its commit tells.
		return outcomeUnknown(err)
	}
	return writeError(st.res, err)
}

// failRaw classifies a database error of the escape hatch's statement,
// keeping the driver's message: the caller holds the connection's whole
// account, and the message is what they are after.
func (st statement) failRaw(err error) error {
	if st.write && uncertain(err) {
		return outcomeUnknown(err)
	}
	kind := ErrDatabase
	if errors.Is(err, store.ErrPermissionDenied) {
		kind = ErrDatasourceForbidden
	}
	return &RawDBError{Kind: kind, Cause: err}
}

// settle follows a write once it ended, whatever its outcome: the database
// may have carried out part of a write that failed, and a procedure may write
// even when its result is unusable. The escape hatch's statement is followed
// whatever its mode: a query may write too (UPDATE ... RETURNING), and what
// it changed is unknown, so every cached read of its database is dropped.
func (st statement) settle(ctx context.Context, err error) error {
	if st.raw {
		st.tc.Writes.Record(st.tc.Session, st.tc.DataSource, st.tc.Sources[st.tc.DataSource].ReadAfterWrite)
		w := writeTargets(st.tc)
		if followErr := applyWrite(st.tc, w, []CacheTarget{{Physical: w.database(st.tc.DataSource)}}); err == nil {
			return followErr
		}
		return err
	}
	if !st.write || st.transactional() { // recorded when admitted
		return err
	}
	if followErr := afterWrite(ctx, st.tc, st.res.Entity, st.tc.Transaction); err == nil {
		return followErr
	}
	return err
}

// ErrOutcomeUnknown marks a write or a commit the database may or may not
// have carried out: its connection broke or its deadline passed while it
// ran. Retrying it blindly may apply it twice.
var ErrOutcomeUnknown = errors.New("tool: the database may or may not have carried out the write")

// outcomeUnknown is ErrOutcomeUnknown with the database error that made the
// outcome unknown, kept for the audit log; clients never see it.
func outcomeUnknown(err error) error { return &classified{kind: ErrOutcomeUnknown, cause: err} }

// uncertain reports whether a failed statement may still have taken effect:
// its connection broke or its deadline passed after it may have been sent. A
// broken connection a driver reports before sending the statement
// (driver.ErrBadConn, which database/sql retries on another connection) is
// not uncertain, nor is a connection that could not be made
// (store.ErrNotConnected), nor any error the database itself returned.
func uncertain(err error) bool {
	var netErr net.Error
	if errors.Is(err, store.ErrNotConnected) {
		return false
	}
	return errors.Is(err, store.ErrConnectionLost) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &netErr)
}
