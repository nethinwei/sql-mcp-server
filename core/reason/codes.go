package reason

// The codes, one place for all: adding an explanation means a constant and
// its English template here, then the console's zh-CN and en messages under
// reasons.<code> (go test ./core/reason -update refreshes the fixture the
// console checks its messages against).
const (
	// Capabilities of the dialects.
	ReturningInsert       Code = "returning.insertReturning"
	ReturningLastInsertID Code = "returning.lastInsertId"
	ExplainPostgresJSON   Code = "explain.postgresJson"
	ExplainMySQLJSON      Code = "explain.mysqlJson"
	ExplainPlanner        Code = "explain.plannerEstimates"
	ExplainCoarse         Code = "explain.coarseEstimates"
	ExplainAnalyze        Code = "explain.analyze"
	ExplainNoAnalyze      Code = "explain.noAnalyze"

	// Statement timeouts and the refusal of unkeyed writes, per connection.
	PostgresTimeout          Code = "timeout.postgresStatementTimeout"
	PostgresTimeoutPooled    Code = "timeout.postgresPooled"
	PostgresCancelOnly       Code = "timeout.postgresCancelOnly"
	SessionVariable          Code = "timeout.sessionVariable"
	SessionVariableReadsOnly Code = "timeout.sessionVariableSelectOnly"
	DSNSetsTimeout           Code = "timeout.dsnSetsVariable"
	MySQLWriteKilled         Code = "timeout.mysqlWriteKilled"
	MySQLWritePooled         Code = "timeout.mysqlWritePooled"
	SettingPooled            Code = "setting.pooled"
	UnkeyedGateOnly          Code = "unkeyedWrite.gateOnly"
	UnkeyedDSNOff            Code = "unkeyedWrite.dsnOff"
	UnkeyedDSNOn             Code = "unkeyedWrite.dsnOn"
	UnkeyedDSNUnclear        Code = "unkeyedWrite.dsnUnclear"
	UnkeyedSafeUpdates       Code = "unkeyedWrite.safeUpdates"
	LimitUnjudged            Code = "limit.unjudged"

	// The work relying on a limit.
	UseReads           Code = "limitUse.reads"
	UseWrites          Code = "limitUse.writes"
	UseReadsAfterWrite Code = "limitUse.readsAfterWrites"
	UseProcedures      Code = "limitUse.procedures"
	UseSQLEscape       Code = "limitUse.sqlEscape"

	// Whether a connection may run an action.
	PrivilegeReadOnlyServer    Code = "privilege.readOnlyServer"
	PrivilegeMissing           Code = "privilege.missing"
	PrivilegeFilterNeedsSelect Code = "privilege.filterNeedsSelect"
	PrivilegeReadsBack         Code = "privilege.readsBack"
	PrivilegeUncheckable       Code = "privilege.uncheckable"

	// How firmly a connection guarantees a capability.
	StrengthEnforced    Code = "strength.enforced"
	StrengthBestEffort  Code = "strength.bestEffort"
	StrengthUnsupported Code = "strength.unsupported"

	// What validating a configuration reports, without refusing it.
	CapabilityDenied Code = "validation.capabilityDenied"
	LimitNotEnforced Code = "validation.limitNotEnforced"
	// A plan grants an action the connection cannot run.
	PlanActionDenied Code = "plan.actionDenied"
)

var templates = map[Code]string{
	ReturningInsert:       "INSERT ... RETURNING",
	ReturningLastInsertID: "no RETURNING: exec + last insert id",
	ExplainPostgresJSON:   "EXPLAIN (FORMAT JSON)",
	ExplainMySQLJSON:      "EXPLAIN FORMAT=JSON",
	ExplainPlanner:        "planner row estimates",
	ExplainCoarse:         "estimates are coarse: the gate scores them only with explain fail-closed",
	ExplainAnalyze:        "EXPLAIN ANALYZE",
	ExplainNoAnalyze:      "no EXPLAIN ANALYZE sampling",

	PostgresTimeout: "statement_timeout just under cost.queryTimeout, and a cancel request at the request deadline",
	PostgresTimeoutPooled: "statement_timeout is a session setting a transaction pooler does not keep; " +
		"a cancel request at the request deadline stops the statement only if the pooler forwards it",
	PostgresCancelOnly:       "a cancel request at the request deadline (the DSN sets its own statement_timeout)",
	SessionVariable:          "{variable} just under cost.queryTimeout",
	SessionVariableReadsOnly: "{variable} just under cost.queryTimeout (SELECT only)",
	DSNSetsTimeout:           "the DSN sets {variable}, so cost.queryTimeout is not applied natively",
	MySQLWriteKilled:         "KILL QUERY at the request deadline (max_execution_time stops SELECT only)",
	MySQLWritePooled: "max_execution_time stops SELECT only, and behind a transaction pooler a statement " +
		"past its deadline cannot be stopped (KILL QUERY could reach another client's session): " +
		"bound writes on the database side (DBA)",
	SettingPooled:   "{variable} is a session setting a transaction pooler does not keep",
	UnkeyedGateOnly: "no database-side refusal; the gate's WriteGuard refuses unkeyed writes",
	UnkeyedDSNOff:   "the DSN turns sql_safe_updates off; the gate's WriteGuard still refuses unkeyed writes",
	UnkeyedDSNOn:    "the DSN sets sql_safe_updates",
	UnkeyedDSNUnclear: "the DSN sets sql_safe_updates to {value}, which cannot be judged; " +
		"the gate's WriteGuard still refuses unkeyed writes",
	UnkeyedSafeUpdates: "sql_safe_updates = 1",
	LimitUnjudged:      "cannot be judged before it is applied: {error}",

	UseReads:           "reads",
	UseWrites:          "writes",
	UseReadsAfterWrite: "reads after writes and in transactions",
	UseProcedures:      "procedures",
	UseSQLEscape:       "the SQL escape hatch",

	PrivilegeReadOnlyServer:    "the server is read-only",
	PrivilegeMissing:           "the account lacks the {privilege} privilege",
	PrivilegeFilterNeedsSelect: "a filtered {privilege} also needs SELECT",
	PrivilegeReadsBack:         "reads after writes run on this connection and need SELECT",
	PrivilegeUncheckable:       "the privilege could not be checked: {error}",

	StrengthEnforced:    "enforced",
	StrengthBestEffort:  "best_effort",
	StrengthUnsupported: "unsupported",

	CapabilityDenied: "entity {entity}: {action} is granted, but connection {connection} cannot run it ({cause})",
	LimitNotEnforced: "hard limit not enforced on the database side: datasource {datasource}, connection " +
		"{connection}: {key} for {use} is {strength} ({cause})",
	PlanActionDenied: "{action}: {cause}",
}
