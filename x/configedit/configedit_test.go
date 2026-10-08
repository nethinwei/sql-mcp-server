package configedit

import (
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

const hash = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

const base = `database: {driver: postgres, dsn: x}
entities:
  - name: orders
    fields: [{name: id}]
users:
  alice:
    tokenHash: ` + hash + `
`

func TestApplyKeepsUnsetSectionsAndMeanings(t *testing.T) {
	t.Parallel()
	cfg, payload, err := Apply([]byte(base), Edit{Entities: []config.EntityConfig{
		{Name: "orders", Fields: []config.FieldConfig{{Name: "id"}}},
		{Name: "hidden", Fields: []config.FieldConfig{{Name: "id"}}, MCP: config.MCPFlagsWithDMLTools(false)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Entities[0].MCP.DMLTools || cfg.Entities[1].MCP.DMLTools {
		t.Fatalf("unset dmlTools must default on and an explicit false stay off: %+v", cfg.Entities)
	}
	if cfg.Users["alice"].TokenHash != hash {
		t.Fatal("a section the edit does not set must stay as it was")
	}
	if !strings.Contains(string(payload), "dmlTools: false") {
		t.Fatalf("payload lost the explicit false:\n%s", payload)
	}
}

func TestApplyUserTokens(t *testing.T) {
	t.Parallel()
	kept, _, err := Apply([]byte(base), Edit{Users: []User{{Name: "Alice", KeepToken: true}}})
	if err != nil || kept.Users["alice"].TokenHash != hash {
		t.Fatalf("KeepToken: %+v, %v", kept.Users["alice"], err)
	}
	revoked, _, err := Apply([]byte(base), Edit{Users: []User{{Name: "alice"}}})
	if err != nil || revoked.Users["alice"].TokenHash != "" {
		t.Fatalf("revoke: %+v, %v", revoked.Users["alice"], err)
	}
	if _, _, err := Apply([]byte(base), Edit{Users: []User{{Name: "alice"}, {Name: "ALICE"}}}); err == nil {
		t.Fatal("a user listed twice must fail")
	}
}

// The edit is validated as a whole: a budget may name a user added by the
// same edit.
func TestApplyValidatesTheWholeResult(t *testing.T) {
	t.Parallel()
	_, _, err := Apply([]byte(base), Edit{
		Users: []User{{Name: "alice", KeepToken: true}, {Name: "bob"}},
		Settings: map[string]any{"budget": map[string]any{
			"users": map[string]any{"bob": map[string]any{"maxConcurrent": 1}},
		}},
	})
	if err != nil {
		t.Fatalf("user and budget added together: %v", err)
	}
	if _, _, err := Apply([]byte(base), Edit{Settings: map[string]any{"entities": []any{}}}); err == nil ||
		!strings.Contains(err.Error(), "settings cannot change") {
		t.Fatalf("settings changing entities: %v", err)
	}
	if _, _, err := Apply([]byte(base), Edit{Settings: map[string]any{"cost": map[string]any{"maxRow": 1}}}); err == nil {
		t.Fatal("unknown setting accepted")
	}
}
