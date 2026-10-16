package bootstrap

import (
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

// callerLimits are the limits of each caller (see config.PrincipalLimits) by
// principal key: a role's own, and a user's own fields over the most
// permissive of its roles'.
func callerLimits(cfg *config.Config) map[string]config.PrincipalLimits {
	out := map[string]config.PrincipalLimits{}
	for name, role := range cfg.Roles {
		if role.Limits != (config.PrincipalLimits{}) {
			out[name] = role.Limits
		}
	}
	for name, user := range cfg.Users {
		if user.Disabled {
			continue
		}
		merged := overLimits(mostPermissiveLimits(cfg.Roles, user.Roles), user.Limits)
		if merged != (config.PrincipalLimits{}) {
			out[UserPrincipal(name)] = merged
		}
	}
	return out
}

// mostPermissiveLimits takes each limit at its most permissive among roles:
// a role leaving it unset leaves it unset. The session budget is merged by
// the budget's own rules (see userBudgets).
func mostPermissiveLimits(roles map[string]config.RoleDefinition, names []string) config.PrincipalLimits {
	if len(names) == 0 {
		return config.PrincipalLimits{}
	}
	merged := roles[names[0]].Limits
	for _, name := range names[1:] {
		l := roles[name].Limits
		merged.MaxRows = looser(merged.MaxRows, l.MaxRows)
		merged.MaxBytes = looser(merged.MaxBytes, l.MaxBytes)
		merged.MaxScannedRows = looser(merged.MaxScannedRows, l.MaxScannedRows)
		merged.QueryTimeout = looser(merged.QueryTimeout, l.QueryTimeout)
		merged.MaxInflight = looser(merged.MaxInflight, l.MaxInflight)
		merged.RPS = looser(merged.RPS, l.RPS)
		merged.SoftScore = lowerScore(merged.SoftScore, l.SoftScore)
		merged.HardScore = lowerScore(merged.HardScore, l.HardScore)
		merged.RejectFullScan = merged.RejectFullScan && l.RejectFullScan
	}
	merged.Budget = config.BudgetLimits{}
	return merged
}

// lowerScore is the more permissive of two gate scores (a lower score lets
// more through), where zero is unset.
func lowerScore(a, b int) int {
	if a == 0 || b == 0 {
		return 0
	}
	return min(a, b)
}

// overLimits is base with the fields own sets.
func overLimits(base, own config.PrincipalLimits) config.PrincipalLimits {
	base.MaxRows = cmpOr(own.MaxRows, base.MaxRows)
	base.MaxBytes = cmpOr(own.MaxBytes, base.MaxBytes)
	base.MaxScannedRows = cmpOr(own.MaxScannedRows, base.MaxScannedRows)
	base.QueryTimeout = cmpOr(own.QueryTimeout, base.QueryTimeout)
	base.SoftScore = cmpOr(own.SoftScore, base.SoftScore)
	base.HardScore = cmpOr(own.HardScore, base.HardScore)
	base.RejectFullScan = own.RejectFullScan || base.RejectFullScan
	base.RPS = cmpOr(own.RPS, base.RPS)
	base.MaxInflight = cmpOr(own.MaxInflight, base.MaxInflight)
	base.Budget = config.BudgetLimits{}
	return base
}

func cmpOr[T int | int64 | float64 | time.Duration](own, base T) T {
	if own != 0 {
		return own
	}
	return base
}

// toolLimitsFor are global as tightened by caller's limits p: a set limit
// only lowers the global one.
func toolLimitsFor(global tool.Limits, p config.PrincipalLimits) tool.Limits {
	global.MaxRows = stricter(global.MaxRows, p.MaxRows)
	global.MaxReturnedBytes = stricter(global.MaxReturnedBytes, p.MaxBytes)
	global.Timeout = stricter(global.Timeout, p.QueryTimeout)
	global.Tightening = cost.Tightening{SoftScore: p.SoftScore, HardScore: p.HardScore, RejectFullScan: p.RejectFullScan,
		MaxScannedRows: p.MaxScannedRows}
	return global
}

// stricter is the lower of two limits, where zero is unlimited.
func stricter[T int64 | time.Duration](global, own T) T {
	switch {
	case own <= 0:
		return global
	case global <= 0:
		return own
	}
	return min(global, own)
}

// keyLimits are the rate and calls in flight of each caller that sets them.
func keyLimits(callers map[string]config.PrincipalLimits) map[string]ratelimit.KeyLimits {
	out := map[string]ratelimit.KeyLimits{}
	for key, p := range callers {
		if p.RPS > 0 || p.MaxInflight > 0 {
			out[key] = ratelimit.KeyLimits{RPS: p.RPS, MaxInflight: p.MaxInflight}
		}
	}
	return out
}

// roleBudgets are each role's session budget: budget.roles with the fields
// its limits.budget sets. A role with neither has none (unlimited).
func roleBudgets(cfg *config.Config) map[string]config.BudgetLimits {
	out := make(map[string]config.BudgetLimits, len(cfg.Budget.Roles))
	for name, limits := range cfg.Budget.Roles {
		out[name] = limits
	}
	for name, role := range cfg.Roles {
		if role.Limits.Budget == (config.BudgetLimits{}) {
			continue
		}
		out[name] = overBudget(out[name], role.Limits.Budget)
	}
	return out
}

// overBudget is base with the fields own sets.
func overBudget(base, own config.BudgetLimits) config.BudgetLimits {
	base.MaxConcurrent = cmpOr(own.MaxConcurrent, base.MaxConcurrent)
	base.MaxExecution = cmpOr(own.MaxExecution, base.MaxExecution)
	base.MaxEstimatedScannedRows = cmpOr(own.MaxEstimatedScannedRows, base.MaxEstimatedScannedRows)
	base.MaxReturnedRows = cmpOr(own.MaxReturnedRows, base.MaxReturnedRows)
	base.MaxReturnedBytes = cmpOr(own.MaxReturnedBytes, base.MaxReturnedBytes)
	base.MaxSessionCost = cmpOr(own.MaxSessionCost, base.MaxSessionCost)
	return base
}
