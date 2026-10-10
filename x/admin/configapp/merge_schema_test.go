package configapp

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"testing"
)

const mergeSchemaFixture = "testdata/merge_schema.json"

// The merge rules, as the console reads them to compare items as the server
// does (web/admin/src/stores/workspace.ts). -update rewrites the fixture.
func TestMergeSchemaFixture(t *testing.T) {
	got, err := json.MarshalIndent(map[string]any{
		"lists": Schema.Lists, "unset": slices.Sorted(maps.Keys(Schema.Unset)),
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile(mergeSchemaFixture, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want, err := os.ReadFile(mergeSchemaFixture); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date (go test ./x/admin/configapp -run %s -update): %v", mergeSchemaFixture, t.Name(), err)
	}
}

// An item as an older codec wrote it, switches off spelled out, means the
// same as one leaving them unset; a switch on by default does not.
func TestZeroSwitchesMeanUnset(t *testing.T) {
	older := decoded(t, `{"name":"orders","fields":[{"name":"id","exclude":false}],"allowCascade":false,
		"mcp":{"dmlTools":true,"customTool":false,"trustedProcedure":false}}`)
	now := decoded(t, `{"name":"orders","fields":[{"name":"id"}],"mcp":{"dmlTools":true}}`)
	if !Schema.Same(older, now) {
		t.Fatal("explicit false switches differ from unset ones")
	}
	if Schema.Same(decoded(t, `{"name":"o","mcp":{"dmlTools":false}}`), decoded(t, `{"name":"o","mcp":{}}`)) {
		t.Fatal("dmlTools, on by default, must not read unset when false")
	}
}
