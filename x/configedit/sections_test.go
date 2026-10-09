package configedit

import (
	"os"
	"slices"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// The console sends only the sections a draft changes: every subset of the
// sections, sent as they are in the base, must give the base back exactly,
// so leaving a section out and sending it unchanged mean the same.
func TestSendingUnchangedSectionsChangesNothing(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../examples/console/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configyaml.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	base, err := revisionops.Normalize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	full := editOf(t, base)
	parts := []func(*Edit){
		func(e *Edit) { e.Entities = full.Entities },
		func(e *Edit) { e.Roles = full.Roles },
		func(e *Edit) { e.Users = full.Users },
		func(e *Edit) { e.Settings = full.Settings },
	}
	for mask := range 1 << len(parts) {
		var edit Edit
		for i, set := range parts {
			if mask&(1<<i) != 0 {
				set(&edit)
			}
		}
		_, payload, err := Apply(base, edit)
		if err != nil {
			t.Fatalf("sections %04b: %v", mask, err)
		}
		if string(payload) != string(base) {
			t.Errorf("sections %04b sent unchanged changed the configuration", mask)
		}
	}
}

// editOf is the whole configuration of payload as an edit, as the console
// would send it.
func editOf(t *testing.T, payload []byte) Edit {
	t.Helper()
	cfg, err := configyaml.Decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(payload, &doc); err != nil {
		t.Fatal(err)
	}
	edit := Edit{Entities: cfg.Entities, Settings: map[string]any{}}
	for _, name := range sortedKeys(cfg.Roles) {
		edit.Roles = append(edit.Roles, Role{Name: name, RoleDefinition: cfg.Roles[name]})
	}
	for _, name := range sortedKeys(cfg.Users) {
		edit.Users = append(edit.Users, User{Name: name, UserConfig: cfg.Users[name]})
	}
	for key, value := range doc {
		if !slices.Contains(structured, key) {
			edit.Settings[key] = value
		}
	}
	if edit.Roles == nil || edit.Users == nil || len(edit.Settings) == 0 {
		t.Fatal("the example configuration lacks a section")
	}
	return edit
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
