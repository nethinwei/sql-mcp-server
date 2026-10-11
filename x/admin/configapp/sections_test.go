package configapp

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

// The admin API's round trip configuration, which covers every field.
const roundTripFixture = "../graph/testdata/roundtrip.yaml"

const sectionsFixture = "testdata/workspace_sections.json"

var update = flag.Bool("update", false, "rewrite "+sectionsFixture)

// A configuration's sections are what a workspace stores and the console
// edits, so their shape must not change unnoticed: workspaces saved before
// would read as changed. Reading them back gives the configuration again.
// -update rewrites the fixture.
func TestSectionsShapeAndRoundTrip(t *testing.T) {
	payload := roundTripPayload(t)
	sections, err := SectionsOfPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(sections)
	if err != nil {
		t.Fatal(err)
	}
	var stored Sections // as a workspace stores and reads it back
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	edit, err := stored.Edit()
	if err != nil {
		t.Fatal(err)
	}
	if _, again, err := configedit.Apply(payload, edit); err != nil || !bytes.Equal(again, payload) {
		t.Fatalf("the sections read back differ: %v\n%s", err, again)
	}
	var canonical any // keys sorted, as a workspace keeps its items
	if err := json.Unmarshal(encoded, &canonical); err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(canonical, "", "  ")
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
		t.Fatalf("%s is out of date (go test ./x/admin/configapp -run %s -update): %v", sectionsFixture, t.Name(), err)
	}
}

// roundTripPayload is the round trip configuration as stored: what it says.
func roundTripPayload(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(roundTripFixture)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := configyaml.DecodeSpec(data)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := revisionops.Normalize(spec)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
