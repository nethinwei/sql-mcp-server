package configyaml

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

// specCorpus are the configurations of the repository and the cases where
// writing a value differs from leaving it out.
func specCorpus(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{
		"explicit zeros and false": []byte(`databases: {shop: {driver: postgres, dsn: x}}
tools: {describeEntities: false}
cost: {maxRows: 7, whitelistPKPoint: false, softScore: 0, rejectFullScan: false, aqe: {readOnly: false}}
entities:
  - {name: a, datasource: shop, mcp: {dmlTools: false}}
  - {name: b, datasource: shop, mcp: {customTool: true}}
  - {name: c, datasource: shop}
`),
		"tools written all false": []byte("databases: {shop: {driver: postgres, dsn: x}}\ntools: {}\n"),
		"legacy single database":  []byte("database: {driver: postgres, dsn: x}\nentities: [{name: a}]\n"),
		"empty lists and maps": []byte("databases: {shop: {driver: postgres, dsn: x}}\n" +
			"budget: {roles: {}}\ncost: {allowTemplates: []}\n"),
		"empty document": []byte(""),
	}
	for _, pattern := range []string{"../../examples/*.yaml", "../../examples/*/config.yaml",
		"../../internal/*/config.yaml", "../../eval/regression/config.yaml", "../admin/graph/testdata/roundtrip.yaml"} {
		paths, _ := filepath.Glob(pattern)
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			out[path] = data
		}
	}
	return out
}

// Resolving what a configuration says gives what decoding it gave before
// Specs: the same configuration in effect, presence included.
func TestResolvedSpecIsTheConfigInEffect(t *testing.T) {
	for name, data := range specCorpus(t) {
		want, wantErr := Decode(data)
		spec, err := DecodeSpec(data)
		if err != nil {
			if wantErr == nil {
				t.Errorf("%s: spec: %v", name, err)
			}
			continue
		}
		got, err := config.Resolve(spec)
		if (err != nil) != (wantErr != nil) {
			t.Errorf("%s: resolve error %v, decode error %v", name, err, wantErr)
			continue
		}
		if err == nil && !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the configuration in effect differs:\n got %+v\nwant %+v", name, got, want)
		}
	}
}

// A payload stored before Specs, every value written out, reads back as a
// Spec that writes it again byte for byte: stored revisions keep their
// content hash.
func TestStoredPayloadsRoundTripAsSpecs(t *testing.T) {
	for name, data := range specCorpus(t) {
		cfg, err := Decode(data)
		if err != nil {
			continue
		}
		payload, err := Encode(cfg)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := DecodeSpec(payload)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		again, err := EncodeSpec(spec)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, payload) {
			t.Errorf("%s: payload changed:\n%s\n---\n%s", name, payload, again)
		}
	}
}

// What is written is what is stored: a Spec writes only what it says, and
// reads back the same.
func TestSpecWritesOnlyWhatItSays(t *testing.T) {
	data := []byte("databases:\n  shop:\n    driver: postgres\n    dsn: x\nentities:\n  - name: a\n    datasource: shop\n")
	spec, err := DecodeSpec(data)
	if err != nil {
		t.Fatal(err)
	}
	out, err := EncodeSpec(spec)
	if err != nil || !bytes.Equal(out, data) {
		t.Fatalf("%v:\n%s", err, out)
	}
}

// A configuration in effect, back as a Spec, resolves to itself; an entity
// keeps a switch on by default unset rather than written out.
func TestSpecOfAConfigInEffect(t *testing.T) {
	for name, data := range specCorpus(t) {
		cfg, err := Decode(data)
		if err != nil {
			continue
		}
		again, err := config.Resolve(config.SpecOf(cfg))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		// The values in effect; which of them were written differs (defaults
		// are written out now).
		want, _ := Encode(cfg)
		got, _ := Encode(again)
		if !bytes.Equal(got, want) {
			t.Errorf("%s: the configuration in effect changed:\n%s\n---\n%s", name, want, got)
		}
	}
	cfg, err := Decode([]byte("databases: {shop: {driver: postgres, dsn: x}}\nentities: [{name: a, datasource: shop}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if mcp := config.SpecOf(cfg).Entities[0].MCP; mcp != nil {
		t.Fatalf("the default dmlTools was written out: %+v", *mcp)
	}
}
