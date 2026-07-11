package tool

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var updateContract = flag.Bool("update", false, "rewrite the tool contract golden file")

const contractGoldenPath = "testdata/contract.json"

// contractSnapshot is the machine-checkable public tool contract: every
// tools/list input schema, the stable error codes with their retryable flag,
// and the field names of the machine-readable denial object.
type contractSnapshot struct {
	Tools        map[string]any  `json:"tools"`
	ErrorCodes   map[string]bool `json:"errorCodes"`
	DenialFields []string        `json:"denialFields"`
}

func currentContract(t *testing.T) contractSnapshot {
	t.Helper()
	schemas := map[string]json.RawMessage{
		"describe_entities": schemaDescribe,
		"read_records":      schemaRead,
		"create_record":     schemaCreate,
		"update_record":     schemaUpdate,
		"delete_record":     schemaDelete,
		"execute_entity":    schemaExecute,
		"aggregate_records": schemaAggregate,
		"begin_transaction": schemaBeginTransaction,
		"transaction_token": transactionTokenSchema,
	}
	tools := make(map[string]any, len(schemas))
	for name, raw := range schemas {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("schema %s is not valid JSON: %v", name, err)
		}
		tools[name] = decoded
	}
	codes := map[string]bool{
		CodeCostExceeded:   true,
		CodeBudgetExceeded: true,
	}
	for _, m := range sentinelDenials {
		codes[m.code] = m.retryable
	}
	return contractSnapshot{Tools: tools, ErrorCodes: codes, DenialFields: denialFieldNames()}
}

func denialFieldNames() []string {
	typ := reflect.TypeOf(Denial{})
	names := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		names = append(names, strings.TrimSuffix(tag, ",omitempty"))
	}
	sort.Strings(names)
	return names
}

// TestToolContractGolden fails when the public tool contract (input schemas,
// error codes, denial fields) drifts from the reviewed snapshot. Intentional
// changes must update the golden explicitly —
// `go test ./core/tool -run TestToolContractGolden -update` — and record the
// change (compatible vs breaking per docs/tool-contract.md) in the CHANGELOG.
func TestToolContractGolden(t *testing.T) {
	snapshot := currentContract(t)
	got, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *updateContract {
		if err := os.MkdirAll(filepath.Dir(contractGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(contractGoldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(contractGoldenPath)
	if err != nil {
		t.Fatalf("read contract golden: %v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("tool contract drifted from the reviewed snapshot.\n" +
			"If this change is intentional, classify it per docs/tool-contract.md, " +
			"note it in the CHANGELOG, and refresh the golden with:\n" +
			"  go test ./core/tool -run TestToolContractGolden -update")
	}
}

// TestContractCanonicalizationIsDeterministic guards the golden mechanism
// itself: two serializations of the same contract must be identical.
func TestContractCanonicalizationIsDeterministic(t *testing.T) {
	a, err := json.Marshal(currentContract(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(currentContract(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("contract serialization is not deterministic")
	}
}
