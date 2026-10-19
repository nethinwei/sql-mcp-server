package graph

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/reason"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// The serving connections' profiles and the limits they leave unenforced
// reach a reader as the runtime reports them.
func TestProviderCapabilities(t *testing.T) {
	h := newHarness(t)
	stubProfiles(h)
	got := queryProviderCapabilities(t, h)
	if len(got.Connections) != 1 || len(got.Connections[0].Claims) != len(dialect.Keys) {
		t.Fatalf("connections = %+v", got.Connections)
	}
	for _, c := range got.Connections[0].Claims {
		if c.Key == string(dialect.TimeoutRead) && (c.Strength != "BEST_EFFORT" ||
			c.Reason != "the DSN sets statement_timeout, so cost.queryTimeout is not applied natively" ||
			c.ReasonDetail == nil || c.ReasonDetail.Code != string(reason.DSNSetsTimeout) ||
			c.ReasonDetail.Params["variable"] != "statement_timeout" || c.ReasonDetail.Text != c.Reason) {
			t.Fatalf("timeout claim = %+v", c)
		}
		if c.Key == string(dialect.Returning) && c.Strength != "UNSUPPORTED" {
			t.Fatalf("an unclaimed capability must read unsupported: %+v", c)
		}
	}
	if len(got.Issues) != 1 || got.Issues[0].Use != "reads" || got.Issues[0].UseDetail.Code != "limitUse.reads" ||
		got.Issues[0].Strength != "BEST_EFFORT" {
		t.Fatalf("issues = %+v", got.Issues)
	}
}

// stubProfiles has the runtime report one connection whose read timeout the
// DSN overrides, which reads rely on.
func stubProfiles(h *harness) {
	h.resolver.App.Profiles = func() ([]bootstrap.ConnectionProfile, []bootstrap.LimitIssue) {
		degraded := dialect.Claim{Strength: dialect.BestEffort, Evidence: dialect.Declared,
			Reason: reason.New(reason.DSNSetsTimeout, "variable", "statement_timeout")}
		return []bootstrap.ConnectionProfile{{Datasource: "shop", Connection: "ro", Profile: dialect.Profile{
				dialect.TimeoutRead: degraded,
			}}}, []bootstrap.LimitIssue{{
				Datasource: "shop", Connection: "ro", Key: dialect.TimeoutRead, Claim: degraded,
				Use: reason.New(reason.UseReads),
			}}
	}
}

type reasonDetail struct {
	Code   string
	Params map[string]string
	Text   string
}

type providerCapabilitiesResult struct {
	Connections []struct {
		Datasource, Connection string
		Claims                 []struct {
			Key, Strength, Evidence, Reason string
			ReasonDetail                    *reasonDetail
		}
	}
	Issues []struct {
		Connection, Key, Use, Strength, Reason string
		UseDetail                              reasonDetail
	}
}

func queryProviderCapabilities(t *testing.T, h *harness) providerCapabilitiesResult {
	t.Helper()
	var resp struct{ ProviderCapabilities providerCapabilitiesResult }
	mustPost(t, h.client(auth.PermRead), `{ providerCapabilities {
		connections { datasource connection claims { key strength evidence reason
			reasonDetail { code params text } } }
		issues { connection key use useDetail { code params text } strength reason } } }`, &resp)
	return resp.ProviderCapabilities
}
