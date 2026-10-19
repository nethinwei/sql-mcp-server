package bootstrap

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

var (
	enforced = dialect.Claim{Strength: dialect.Enforced}
	degraded = dialect.Claim{Strength: dialect.BestEffort, Reason: "the DSN sets it"}
)

// limitedConnection is a connection whose timeouts are the given claims.
func limitedConnection(read, write dialect.Claim) *catalogProvider {
	p := relationProvider("srv", false, entity.Entity{Name: "users", Source: "users"})
	p.limits = dialect.Profile{dialect.TimeoutRead: read, dialect.TimeoutWrite: write}
	return p
}

func limitsConfig(actions ...string) *config.Config {
	cfg := connectionsConfig(config.EntityConfig{
		Name: "users", DataSource: "main", Fields: []config.FieldConfig{{Name: "id"}},
	})
	cfg.Tools = config.DefaultToolFlags()
	cfg.Tools.CreateRecord, cfg.Tools.UpdateRecord = true, true
	if len(actions) > 0 {
		cfg.Roles = map[string]config.RoleDefinition{"writer": {
			Grants: []config.GrantConfig{{Entity: "users", Actions: actions}},
		}}
	}
	return cfg
}

func issueKeys(issues []LimitIssue) []string {
	out := make([]string, len(issues))
	for i, issue := range issues {
		out[i] = issue.Connection + " " + string(issue.Key) + " " + issue.Use
	}
	return out
}

type routedWorkCase struct {
	name    string
	actions []string
	edit    func(*config.Config)
	want    []string
}

// routedWorkCases are configurations of one datasource and the issues each
// must report against limitedConnections.
func routedWorkCases() []routedWorkCase {
	reads := "replica limit.timeout.read reads"
	procedure := func(trusted bool) func(*config.Config) {
		return func(cfg *config.Config) {
			cfg.Entities = append(cfg.Entities, config.EntityConfig{
				Name: "refresh", Kind: "procedure", DataSource: "main",
				MCP: config.MCPFlags{TrustedProcedure: trusted, CustomTool: true},
			})
			cfg.Roles["writer"] = config.RoleDefinition{Grants: []config.GrantConfig{
				{Entity: "refresh", Actions: []string{"execute"}},
			}}
		}
	}
	escape := func(cfg *config.Config) {
		cfg.Tools.ExecuteSQL = true
		db := cfg.Databases["main"]
		db.SQLEscape.Connection = "rw"
		cfg.Databases["main"] = db
	}
	return []routedWorkCase{
		{"reads only", nil, nil, []string{reads}},
		{"writes granted", []string{"read", "update"}, nil, []string{
			"rw limit.timeout.read reads after writes and in transactions", reads, "rw limit.timeout.write writes",
		}},
		{"write tool off", []string{"update"}, func(cfg *config.Config) { cfg.Tools.UpdateRecord = false },
			[]string{reads}},
		{"dml tools off", []string{"update"}, func(cfg *config.Config) { cfg.Entities[0].MCP.DMLTools = false },
			[]string{reads}},
		{"trusted procedure granted", []string{"read"}, procedure(true),
			[]string{reads, "ro limit.timeout.write procedures"}},
		{"untrusted procedure granted", []string{"read"}, procedure(false), []string{reads}},
		{"escape hatch on", nil, escape,
			[]string{reads, "rw limit.timeout.read the SQL escape hatch", "rw limit.timeout.write the SQL escape hatch"}},
	}
}

// Each connection is held to the limits of the work routed to it: writes and
// procedures only when someone may run them through a tool that is on, reads
// on the write connection when they follow writes there, the escape hatch
// only while on.
func TestNativeLimitIssuesFollowTheRoutedWork(t *testing.T) {
	t.Parallel()
	conns := Connections{"main": {
		"replica": limitedConnection(degraded, degraded),
		"rw":      limitedConnection(degraded, degraded),
		"ro":      limitedConnection(enforced, degraded),
	}}
	for _, test := range routedWorkCases() {
		cfg := limitsConfig(test.actions...)
		if cfg.Roles == nil {
			cfg.Roles = map[string]config.RoleDefinition{}
		}
		if test.edit != nil {
			test.edit(cfg)
		}
		got := issueKeys(NativeLimitIssues(cfg, conns))
		sort.Strings(got)
		want := append([]string{}, test.want...)
		sort.Strings(want)
		if strings.Join(got, ";") != strings.Join(want, ";") {
			t.Errorf("%s: issues = %q, want %q", test.name, got, want)
		}
	}
}

// Off, an unenforced limit only warns; on, the configuration is refused with
// each issue named.
func TestRequireNativeLimitsRefusesTheConfiguration(t *testing.T) {
	t.Parallel()
	conns := func() Connections {
		return Connections{"main": {
			"replica": limitedConnection(degraded, enforced),
			"rw":      limitedConnection(enforced, enforced), "ro": limitedConnection(enforced, enforced),
		}}
	}
	app, err := AssembleWithConnections(limitsConfig(), conns())
	if err != nil {
		t.Fatalf("off, the configuration must load: %v", err)
	}
	_ = app.Close()

	strict := limitsConfig()
	strict.Cost.RequireNativeLimits = true
	_, err = AssembleWithConnections(strict, conns())
	if !errors.Is(err, ErrNativeLimits) || !strings.Contains(err.Error(), `connection "replica"`) ||
		!strings.Contains(err.Error(), "the DSN sets it") {
		t.Fatalf("err = %v", err)
	}

	enforcedEverywhere := Connections{"main": {
		"replica": limitedConnection(enforced, enforced),
		"rw":      limitedConnection(enforced, enforced), "ro": limitedConnection(enforced, enforced),
	}}
	app, err = AssembleWithConnections(strict, enforcedEverywhere)
	if err != nil {
		t.Fatalf("enforced limits must load under strict mode: %v", err)
	}
	_ = app.Close()
}

const plannedTestDriver = "bootstrap-planned-limits-test"

func init() {
	providerregistry.RegisterLimits(plannedTestDriver,
		func(dsn string, opts providerregistry.Options) (dialect.Profile, error) {
			if dsn == "pooled" || opts.Pooler != "" {
				return dialect.Profile{dialect.TimeoutRead: degraded, dialect.TimeoutWrite: degraded}, nil
			}
			return dialect.Profile{dialect.TimeoutRead: enforced, dialect.TimeoutWrite: enforced}, nil
		})
}

// A configuration not yet applied is judged from its DSNs, resolved as
// serving resolves them, without connecting; what cannot be judged is an
// issue of its own.
func TestPlannedLimitIssues(t *testing.T) {
	t.Setenv("PLANNED_DSN", "pooled")
	app := &App{scanResolver: EnvFileResolver{}}
	judge := func(driver, dsn string) []string {
		cfg := &config.Config{Databases: map[string]config.DatabaseConfig{"main": {Driver: driver, DSN: dsn}}}
		cfg.ApplyDefaults()
		var out []string
		for _, issue := range app.PlannedLimitIssues(cfg) {
			out = append(out, string(issue.Key)+" "+issue.Claim.Reason)
		}
		return out
	}
	if got := judge(plannedTestDriver, "direct"); len(got) != 0 {
		t.Fatalf("an enforced connection: %q", got)
	}
	if got := judge(plannedTestDriver, "${PLANNED_DSN}"); len(got) != 1 || !strings.Contains(got[0], "the DSN sets it") {
		t.Fatalf("the resolved DSN must be judged: %q", got)
	}
	if got := judge(plannedTestDriver, "${PLANNED_UNSET_DSN}"); len(got) != 1 ||
		!strings.Contains(got[0], "cannot be judged") {
		t.Fatalf("an unresolvable DSN: %q", got)
	}
	if got := judge("bootstrap-no-limits-driver", "x"); len(got) != 1 ||
		!strings.Contains(got[0], "declares no limits") {
		t.Fatalf("a driver without limits: %q", got)
	}
}

// Behind a transaction pooler PostgreSQL's timeouts are best effort, so
// cost.requireNativeLimits refuses the connection; a direct one passes.
func TestPlannedLimitIssuesRefusePostgresBehindATransactionPooler(t *testing.T) {
	t.Parallel()
	app := &App{scanResolver: EnvFileResolver{}}
	for pooler, want := range map[string]int{"": 0, providerregistry.PoolerTransaction: 1} {
		cfg := &config.Config{Databases: map[string]config.DatabaseConfig{
			"main": {Driver: "postgres", Connections: config.Connections{
				"primary": {DSN: "postgres://u@h/db", Pooler: pooler},
			}},
		}}
		cfg.ApplyDefaults()
		issues := app.PlannedLimitIssues(cfg)
		if len(issues) != want {
			t.Fatalf("pooler %q: issues %v, want %d", pooler, issues, want)
		}
		for _, issue := range issues {
			if issue.Claim.Strength != dialect.BestEffort || !strings.Contains(issue.Claim.Reason, "transaction pooler") {
				t.Fatalf("pooler %q: %v", pooler, issue)
			}
		}
	}
}

// A process logs its hard-limit warnings again only when they change.
func TestHardLimitWarningsAreLoggedWhenTheyChange(t *testing.T) {
	var s Shared
	if !s.unwarned("a") || s.unwarned("a") || !s.unwarned("b") || !s.unwarned("") || !s.unwarned("b") {
		t.Fatal("warnings must be logged exactly when they change")
	}
	var none *Shared
	if first, again := none.unwarned("a"), none.unwarned("a"); !first || !again {
		t.Fatal("without a Shared every assembly logs")
	}
}
