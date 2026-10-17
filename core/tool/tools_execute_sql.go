package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
)

// ---- execute_sql ----

// ActionSQL is the audited action of the SQL escape hatch.
const ActionSQL = "sql"

const executeSQLName = "execute_sql"

// ErrAuditUnavailable is an escape hatch call refused, or its result
// withheld, because its audit record could not be written: the hatch is
// audited fail closed.
var ErrAuditUnavailable = errors.New("tool: the audit log is unavailable; execute_sql is audited fail closed")

const executeSQLDescription = "Run one SQL statement as it is on a datasource that admits it (trusted escape " +
	"hatch). The statement is not parsed, rewritten or masked: the connection's database account bounds what it " +
	"can do, and this tool is listed only for users holding sql:execute@<datasource>. Every call is recorded in " +
	"the audit log before and after it runs, with the statement as sent; a call the log cannot record does not " +
	"run. mode query returns columns and rows (cut at the row cap, with truncated: true); mode exec returns " +
	"rowsAffected. Each call is one auto-committed statement under the server's timeout, row, byte and budget " +
	"limits; transaction tokens are not accepted. Database errors are reported with the driver's message."

// ExecuteSQLTool is the trusted SQL escape hatch: it runs one statement as
// it is on a datasource admitting it, for a user holding
// sql:execute@<datasource>, under the governed plane's hard limits and a
// fail-closed audit. It has no entity: nothing is masked or filtered, and
// no IR, metric or extension reaches it.
type ExecuteSQLTool struct{}

// Info implements Tool.
func (ExecuteSQLTool) Info() Info {
	return Info{
		Name: executeSQLName, Title: "Execute SQL", Description: executeSQLDescription,
		InputSchema: schemaExecuteSQL, Action: ActionSQL, Destructive: true, Object: true,
	}
}

// Enabled implements Tool: the hatch is registered by tools.executeSql only.
func (ExecuteSQLTool) Enabled(f config.ToolFlags) bool { return f.ExecuteSQL }

func (ExecuteSQLTool) durablyAudited() {}

// durablyAudited marks a tool whose audit record is written before its
// result is returned, and whose result is withheld when the record fails.
type durablyAudited interface {
	Tool
	durablyAudited()
}

type executeSQLInput struct {
	Datasource string `json:"datasource"`
	SQL        string `json:"sql"`
	Args       []any  `json:"args,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

func (in *executeSQLInput) normalize() error {
	in.Datasource, in.SQL = strings.TrimSpace(in.Datasource), strings.TrimSpace(in.SQL)
	if in.Datasource == "" {
		return errors.New("datasource is required")
	}
	if in.SQL == "" {
		return errors.New("sql is required")
	}
	switch in.Mode {
	case "":
		in.Mode = "query"
	case "query", "exec":
	default:
		return fmt.Errorf("mode %q: want query or exec", in.Mode)
	}
	if in.Args != nil {
		normalized, err := normalizeJSONValue(in.Args)
		if err != nil {
			return err
		}
		in.Args, _ = normalized.([]any)
	}
	return nil
}

// Run implements Tool.
func (ExecuteSQLTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	in, err := decodeTool(input, (*executeSQLInput).normalize)
	if err != nil {
		return Result{}, err
	}
	trace := escapeTraceOf(ctx)
	trace.datasource, trace.sql = in.Datasource, in.SQL
	// The tool is not read-only (a statement may write), so RunTool metered
	// it as a write; a query is metered afresh, against the byte limits.
	if in.Mode == "query" {
		tc.results = nil
	}
	ctx, tc, end := beginCall(ctx, tc, in.Mode == "query", "")
	defer end()
	g, route, err := authorizeSQL(tc, in.Datasource)
	if err != nil {
		return Result{}, err
	}
	trace.connection = route.Connection
	tc.DB, tc.Dialect, tc.DataSource = route.DB, route.Dialect, in.Datasource
	sql, err := estimateSQL(ctx, tc, route, in.SQL, in.Args, in.Mode == "query")
	if err != nil {
		return Result{}, err
	}
	trace.plan = sql.plan
	st := newRawStatement(tc, g, sql, in.Mode == "exec")
	if err := auditSQLBegin(ctx, tc, g, trace); err != nil {
		return Result{}, err
	}
	if in.Mode == "exec" {
		r, err := st.exec(ctx)
		if err != nil {
			return Result{}, err
		}
		trace.rowsAffected = r.RowsAffected
		return Result{Content: []map[string]any{{"rowsAffected": r.RowsAffected}}, Counted: true}, nil
	}
	got, err := st.queryRaw(ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Content: []map[string]any{{
			"columns": got.columns, "rows": got.rows, "rowCount": len(got.rows), "truncated": got.truncated,
		}},
		ReturnedRows: int64(len(got.rows)), Counted: true,
	}, nil
}

// escapeTrace is what an execute_sql call adds to its audit events, filled
// in as the call goes, so that its "end" event tells what ran even when the
// call failed (a failed call returns no Result through the engine).
type escapeTrace struct {
	datasource, connection, sql string
	rowsAffected                int64
	plan                        *cost.Plan
}

type escapeTraceKey struct{}

// withEscapeTrace gives a durably audited call its trace.
func withEscapeTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, escapeTraceKey{}, &escapeTrace{})
}

// escapeTraceOf is the call's trace, or one of the call's own when it runs
// outside RunTool.
func escapeTraceOf(ctx context.Context) *escapeTrace {
	if trace, ok := ctx.Value(escapeTraceKey{}).(*escapeTrace); ok {
		return trace
	}
	return &escapeTrace{}
}

// fill sets the hatch's fields on ev for phase.
func (t *escapeTrace) fill(ev *audit.Event, phase string) {
	ev.Phase, ev.Datasource, ev.Connection, ev.SQL = phase, t.datasource, t.connection, t.sql
	ev.RowsAffected, ev.Cost = t.rowsAffected, t.plan
}

// isMetadataStatement reports a single SHOW, DESCRIBE or DESC statement,
// which EXPLAIN cannot estimate and which reads the catalog alone (see
// cost.sqlEscape.metadata). It recognizes how the statement begins, after
// whitespace and comments, and that no other statement follows; it does not
// parse SQL, and anything it does not recognize is estimated as before.
func isMetadataStatement(sql string) bool {
	rest := skipSpaceAndComments(sql)
	word := rest
	if i := strings.IndexFunc(rest, func(r rune) bool { return !unicode.IsLetter(r) }); i >= 0 {
		word = rest[:i]
	}
	keyword := strings.ToUpper(word)
	if keyword != "SHOW" && keyword != "DESCRIBE" && keyword != "DESC" {
		return false
	}
	if len(word) == len(rest) || !unicode.IsSpace(rune(rest[len(word)])) {
		return false
	}
	body := strings.TrimRight(strings.TrimSpace(rest), "; \t\r\n")
	if strings.Contains(body, ";") {
		return false
	}
	// DESCRIBE and DESC are also EXPLAIN on MySQL and OceanBase, and
	// DESCRIBE ANALYZE runs the statement: only DESCRIBE <table> is one.
	return keyword == "SHOW" || describesTable(skipSpaceAndComments(rest[len(word):]))
}

// explainWords are what may follow DESCRIBE when it is EXPLAIN.
var explainWords = map[string]bool{
	"ANALYZE": true, "FORMAT": true, "EXTENDED": true, "PARTITIONS": true, "FOR": true, "SELECT": true,
	"TABLE": true, "WITH": true, "INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true, "VALUES": true,
}

// describesTable reports that what follows DESCRIBE names a table: it
// begins with a (possibly quoted) name that is not an EXPLAIN keyword.
func describesTable(rest string) bool {
	if rest == "" {
		return false
	}
	if rest[0] == '`' || rest[0] == '"' {
		return true
	}
	end := strings.IndexFunc(rest, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	if end < 0 {
		end = len(rest)
	}
	return end > 0 && !explainWords[strings.ToUpper(rest[:end])]
}

// skipSpaceAndComments drops leading whitespace and --, # and /* */ comments.
func skipSpaceAndComments(s string) string {
	for {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
		switch {
		case strings.HasPrefix(s, "--"), strings.HasPrefix(s, "#"):
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				return ""
			}
			s = s[i+1:]
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s, "*/")
			if i < 0 {
				return ""
			}
			s = s[i+2:]
		default:
			return s
		}
	}
}

// UnestimableError is a statement the escape hatch could not estimate: the
// datasource's EXPLAIN failed on it (DDL, SET, SHOW, a syntax error, an
// error raised while planning). It is refused while
// cost.sqlEscape.estimate is required, as cost exceeded, with the
// database's message first: that is what the caller has to act on.
type UnestimableError struct{ Cause error }

func (e *UnestimableError) Error() string {
	return "execute_sql: EXPLAIN failed: " + e.Cause.Error()
}

// Unwrap returns cost.ErrCostExceeded: an estimate the gate requires is missing.
func (e *UnestimableError) Unwrap() error { return cost.ErrCostExceeded }

// RawDBError is a database error of the escape hatch's statement, reported
// with the driver's message: the caller holds the connection's whole
// account, so the message adds nothing it could not see, and it is what
// they are after. It is its Kind (ErrDatabase or ErrDatasourceForbidden) to
// errors.Is and unwraps to the driver's error.
type RawDBError struct {
	Kind  error
	Cause error
}

func (e *RawDBError) Error() string        { return e.Kind.Error() + ": " + e.Cause.Error() }
func (e *RawDBError) Is(target error) bool { return target == e.Kind }
func (e *RawDBError) Unwrap() error        { return e.Cause }

// SQLState is the SQLSTATE of the driver's error when it reports one.
func (e *RawDBError) SQLState() string {
	var stated interface{ SQLState() string }
	if errors.As(e.Cause, &stated) {
		return stated.SQLState()
	}
	return ""
}

// escapeDenial maps the escape hatch's own errors to the rejection contract.
func escapeDenial(err error) (Denial, bool) {
	var unestimable *UnestimableError
	if errors.As(err, &unestimable) {
		return Denial{
			Code: CodeCostExceeded, Reason: unestimable.Error(), Retryable: true,
			Hints: []string{"rewrite the statement so that EXPLAIN accepts it; statements EXPLAIN cannot " +
				"estimate run only with cost.sqlEscape.estimate: skip"},
		}, true
	}
	var raw *RawDBError
	if !errors.As(err, &raw) {
		return Denial{}, false
	}
	code := CodeDatabaseError
	if raw.Kind == ErrDatasourceForbidden {
		code = CodeDatasourceForbidden
	}
	d := Denial{Code: code, Reason: raw.Error(), Retryable: false}
	if state := raw.SQLState(); state != "" {
		d.Constraints = map[string]any{"sqlstate": state}
	}
	return d, true
}
