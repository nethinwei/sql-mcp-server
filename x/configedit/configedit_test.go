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
	off := false
	cfg, payload, err := Apply([]byte(base), Edit{Entities: []config.EntitySpec{
		{Name: ptr("orders"), Fields: config.List[config.FieldSpec]{{Name: ptr("id")}}},
		{Name: ptr("hidden"), Fields: config.List[config.FieldSpec]{{Name: ptr("id")}},
			MCP: &config.MCPFlagsSpec{DMLTools: &off}},
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
	if !strings.Contains(string(payload), "dmlTools: false") || strings.Count(string(payload), "dmlTools") != 1 {
		t.Fatalf("the payload must keep the explicit false and leave the default unset:\n%s", payload)
	}
}

func ptr[T any](v T) *T { return &v }

// A user is as written, its token included: one without a hash has none.
func TestApplyUserTokens(t *testing.T) {
	t.Parallel()
	alice := User{Name: "Alice", UserSpec: config.UserSpec{TokenHash: ptr(hash)}}
	kept, _, err := Apply([]byte(base), Edit{Users: []User{alice}})
	if err != nil || kept.Users["alice"].TokenHash != hash {
		t.Fatalf("kept: %+v, %v", kept.Users["alice"], err)
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
		Users: []User{{Name: "alice", UserSpec: config.UserSpec{TokenHash: ptr(hash)}}, {Name: "bob"}},
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
