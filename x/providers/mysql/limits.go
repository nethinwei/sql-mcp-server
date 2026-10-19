package mysql

import (
	"fmt"
	"strconv"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/reason"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

// Limits is what a MySQL connection to dsn opened with opts guarantees on
// the server, without connecting.
func Limits(dsn string, opts providerregistry.Options) (dialect.Profile, error) {
	return LimitsFor(dsn, opts, "")
}

// LimitsFor is Limits for a MySQL-compatible database whose statement
// timeout is the named variable (OceanBase's ob_query_timeout), or
// max_execution_time when it is empty.
func LimitsFor(dsn string, opts providerregistry.Options, timeoutVariable string) (dialect.Profile, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("mysql: parse dsn: %w", err)
	}
	return limits(cfg.Params, opts.Pooler, timeoutVariable), nil
}

// limits judges the session settings NewAdapter injects against what params
// (the DSN's own) already set. Behind a transaction pooler the session a
// setting lands on is not the one the next statement runs in. A cancelled
// context only closes the client connection: the statement may run on, so
// nothing on the server stops a statement the variables do not.
func limits(params map[string]string, pooler, timeoutVariable string) dialect.Profile {
	pooled := pooler == providerregistry.PoolerTransaction
	session := func(variable string, enforced reason.Code) dialect.Claim {
		if pooled {
			return dialect.Claim{Strength: dialect.BestEffort, Evidence: dialect.Declared,
				Reason: reason.New(reason.SettingPooled, "variable", variable)}
		}
		if _, set := params[variable]; set {
			return dialect.Claim{Strength: dialect.BestEffort, Evidence: dialect.Declared,
				Reason: reason.New(reason.DSNSetsTimeout, "variable", variable)}
		}
		return dialect.Claim{Strength: dialect.Enforced, Evidence: dialect.DBVerified,
			Reason: reason.New(enforced, "variable", variable)}
	}
	out := dialect.Profile{}
	if timeoutVariable == "" {
		out[dialect.TimeoutRead] = session("max_execution_time", reason.SessionVariableReadsOnly)
		out[dialect.TimeoutWrite] = dialect.Claim{Strength: dialect.Unsupported, Evidence: dialect.Declared,
			Reason: reason.New(reason.MySQLWriteTimeout)}
	} else {
		out[dialect.TimeoutRead] = session(timeoutVariable, reason.SessionVariable)
		out[dialect.TimeoutWrite] = session(timeoutVariable, reason.SessionVariable)
	}
	out[dialect.UnkeyedWrite] = unkeyedWrite(params, pooled)
	return out
}

// unkeyedWrite judges sql_safe_updates: only a value the DSN plainly turns
// on (1, ON) counts as on, as a value not understood here cannot be judged.
func unkeyedWrite(params map[string]string, pooled bool) dialect.Claim {
	raw, set := params["sql_safe_updates"]
	value := strings.ToLower(strings.Trim(raw, `'"`))
	switch {
	case set && (value == "0" || value == "off"):
		return dialect.Claim{Strength: dialect.Unsupported, Evidence: dialect.Declared,
			Reason: reason.New(reason.UnkeyedDSNOff)}
	case pooled:
		return dialect.Claim{Strength: dialect.BestEffort, Evidence: dialect.Declared,
			Reason: reason.New(reason.SettingPooled, "variable", "sql_safe_updates")}
	case set && (value == "1" || value == "on"):
		return dialect.Claim{Strength: dialect.Enforced, Evidence: dialect.Declared,
			Reason: reason.New(reason.UnkeyedDSNOn)}
	case set:
		return dialect.Claim{Strength: dialect.BestEffort, Evidence: dialect.Declared,
			Reason: reason.New(reason.UnkeyedDSNUnclear, "value", strconv.Quote(raw))}
	}
	return dialect.Claim{Strength: dialect.Enforced, Evidence: dialect.DBVerified,
		Reason: reason.New(reason.UnkeyedSafeUpdates)}
}
