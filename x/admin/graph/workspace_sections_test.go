package graph

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

const sectionsFixture = "testdata/workspace_sections.json"

var update = flag.Bool("update", false, "rewrite "+sectionsFixture)

// The round trip fixture as the console reads it (configuration, the read
// model) and as WorkspaceSections writes it (sections): the console builds
// the same sections from the same configuration
// (web/admin/src/stores/workspace.encoding.test.ts), so what an agent writes
// into a workspace reads in the console as if made there. Reading the
// sections back gives the configuration again. -update rewrites the fixture.
func TestWorkspaceSectionsAsTheConsoleBuildsThem(t *testing.T) {
	data, err := os.ReadFile(roundTripFixture)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configyaml.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := revisionops.Normalize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	read, err := toConfiguration(cfg, payload)
	if err != nil {
		t.Fatal(err)
	}
	sections, err := WorkspaceSections(cfg, payload)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := WorkspaceEdit(sections)
	if err != nil {
		t.Fatal(err)
	}
	if _, again, err := configedit.Apply(payload, edit); err != nil || !bytes.Equal(again, payload) {
		t.Fatalf("the sections read back differ: %v\n%s", err, again)
	}
	got, err := json.MarshalIndent(map[string]any{
		"configuration": map[string]any{
			"entities": read.Entities, "roles": read.Roles, "users": read.Users, "settings": read.Settings,
		},
		"sections": json.RawMessage(sections),
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile(sectionsFixture, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(sectionsFixture)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date (go test ./x/admin/graph -run %s -update): %v", sectionsFixture, t.Name(), err)
	}
}
