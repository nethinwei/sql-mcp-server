package tool

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
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

// authorizeRequest authorizes req, refusing it unless allowed.
func authorizeRequest(ctx context.Context, tc Context, req rbac.Request) (grant, error) {
	dec, err := authorize(ctx, tc, req)
	if err != nil {
		return grant{}, err
	}
	if !dec.Allowed {
		return grant{}, denyUnauthorized(dec)
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

// estimatedRows is the rows the gate estimated the statement scans.
func (g gated) estimatedRows() int64 {
	if g.plan == nil {
		return 0
	}
	return g.plan.EstimatedRows
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
	// rowCap bounds the rows of the statement (0: no bound of its own).
	rowCap int64
	write  bool // a write: its errors and its effects are followed
}

// newStatement plans running sql on tc for res under g.
func newStatement(tc Context, res entity.Resolved, g grant, sql gated) statement {
	return statement{tc: tc, res: res, grant: g, sql: sql}
}

// admitted refuses a statement made without an authorization or without SQL
// (the zero grant or gated a failed step returns).
func (st statement) admitted() error {
	if !st.grant.Allowed || st.sql.SQL == "" {
		return ErrUnauthorized
	}
	return nil
}

// queried are a query's rows and, for each, the values of the statement's
// kept columns as read.
type queried struct {
	rows []map[string]any
	kept []map[string]any
}

// query runs st and reads its rows: shaped, masked and counted against the
// call's limits, stopping at the first that crosses them.
func (st statement) query(ctx context.Context) (out queried, err error) {
	if err := st.admitted(); err != nil {
		return queried{}, err
	}
	defer func() { err = st.settle(ctx, err) }()
	rows, err := st.tc.DB.QueryContext(ctx, st.sql.SQL, st.sql.Args...)
	if err != nil {
		return queried{}, st.fail(err)
	}
	out.rows = make([]map[string]any, 0)
	for row, err := range store.Iter(rows) {
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
		st.present(row)
		if st.rowCap > 0 && int64(len(out.rows)) >= st.rowCap {
			return queried{}, budget.ErrExceeded
		}
		if err := st.tc.results.add(row); err != nil {
			return queried{}, err
		}
		out.rows = append(out.rows, row)
	}
	return out, nil
}

// exec runs st, a statement without rows.
func (st statement) exec(ctx context.Context) (store.Result, error) {
	if err := st.admitted(); err != nil {
		return store.Result{}, err
	}
	r, err := st.tc.DB.ExecContext(ctx, st.sql.SQL, st.sql.Args...)
	if err != nil {
		err = st.fail(err)
	}
	return r, st.settle(ctx, err)
}

// present shapes and masks a row of st's entity for the caller.
func (st statement) present(row map[string]any) {
	if st.fields != nil {
		for field := range row {
			if !st.fields[field] {
				delete(row, field)
			}
		}
	}
	if st.tc.Masker != nil {
		maskRow(st.tc.Masker, st.res.Entity.Attributes, row)
	}
}

// fail classifies a database error of st.
func (st statement) fail(err error) error {
	if !st.write {
		return WrapDBError(err)
	}
	if st.tc.Transaction == "" && uncertain(err) {
		// Inside a transaction nothing is committed yet; its commit tells.
		return outcomeUnknown(err)
	}
	return writeError(st.res, err)
}

// settle follows a write once it ended, whatever its outcome: the database
// may have carried out part of a write that failed, and a procedure may write
// even when its result is unusable.
func (st statement) settle(ctx context.Context, err error) error {
	if !st.write {
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
// not uncertain, nor is any error the database itself returned.
func uncertain(err error) bool {
	var netErr net.Error
	return errors.Is(err, store.ErrConnectionLost) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &netErr)
}
