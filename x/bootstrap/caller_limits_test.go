package bootstrap

import (
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

func callerLimitsConfig() *config.Config {
	return &config.Config{
		Roles: map[string]config.RoleDefinition{
			"bi": {Limits: config.PrincipalLimits{
				MaxRows: 500, QueryTimeout: 5 * time.Second, SoftScore: 40, RPS: 2, RejectFullScan: true,
				Budget: config.BudgetLimits{MaxConcurrent: 2},
			}},
			"ops": {Limits: config.PrincipalLimits{MaxRows: 2000, SoftScore: 20, RPS: 10, RejectFullScan: true}},
			"any": {},
		},
		Users: map[string]config.UserConfig{
			"ana":  {Roles: []string{"bi", "ops"}},
			"bot":  {Roles: []string{"bi"}, Limits: config.PrincipalLimits{MaxRows: 100, MaxInflight: 1}},
			"free": {Roles: []string{"bi", "any"}},
		},
		Budget: config.BudgetConfig{Roles: map[string]config.BudgetLimits{"bi": {MaxConcurrent: 9, MaxSessionCost: 7}}},
	}
}

// Several roles give each limit at its most permissive (a role leaving it
// unset leaves it unset); a user's own fields win.
func TestCallerLimitsMerge(t *testing.T) {
	t.Parallel()
	got := callerLimits(callerLimitsConfig())
	ana := got["user:ana"]
	if ana.MaxRows != 2000 || ana.QueryTimeout != 0 || ana.SoftScore != 20 || ana.RPS != 10 || !ana.RejectFullScan {
		t.Fatalf("ana = %+v", ana)
	}
	if bot := got["user:bot"]; bot.MaxRows != 100 || bot.MaxInflight != 1 || bot.RPS != 2 || bot.SoftScore != 40 {
		t.Fatalf("bot = %+v", bot)
	}
	if _, ok := got["user:free"]; ok {
		t.Fatalf("a user with a role without limits has none: %+v", got["user:free"])
	}
	if got["bi"].MaxRows != 500 {
		t.Fatalf("role bi = %+v", got["bi"])
	}
	if k := keyLimits(got); k["user:bot"] != (ratelimit.KeyLimits{RPS: 2, MaxInflight: 1}) {
		t.Fatalf("key limits = %+v", k)
	}
}

// A caller's limits only tighten the global ones.
func TestToolLimitsOnlyTighten(t *testing.T) {
	t.Parallel()
	global := tool.Limits{MaxRows: 1000, MaxReturnedBytes: 1 << 20, Timeout: 10 * time.Second}
	got := toolLimitsFor(global, config.PrincipalLimits{MaxRows: 5000, QueryTimeout: time.Second, SoftScore: 30})
	want := tool.Limits{
		MaxRows: 1000, MaxReturnedBytes: 1 << 20, Timeout: time.Second, Tightening: cost.Tightening{SoftScore: 30},
	}
	if got != want {
		t.Fatalf("limits = %+v, want %+v", got, want)
	}
	if got := toolLimitsFor(tool.Limits{}, config.PrincipalLimits{QueryTimeout: time.Second}); got.Timeout != time.Second {
		t.Fatalf("an unlimited global timeout = %v", got.Timeout)
	}
}

// A role's limits.budget fields win over budget.roles; a user's over both.
func TestSessionBudgetsFromLimits(t *testing.T) {
	t.Parallel()
	cfg := callerLimitsConfig()
	cfg.Users["bot"] = config.UserConfig{Roles: []string{"bi"}, Limits: config.PrincipalLimits{
		Budget: config.BudgetLimits{MaxSessionCost: 3},
	}}
	if bi := roleBudgets(cfg)["bi"]; bi.MaxConcurrent != 2 || bi.MaxSessionCost != 7 {
		t.Fatalf("bi = %+v", bi)
	}
	if bot := userBudgets(cfg)["user:bot"]; bot.MaxConcurrent != 2 || bot.MaxSessionCost != 3 {
		t.Fatalf("bot = %+v", bot)
	}
}
