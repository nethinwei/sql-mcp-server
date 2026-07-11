package tool

import (
	"encoding/json"
	"testing"
)

func TestSchemasValidObject(t *testing.T) {
	t.Parallel()
	for name, s := range map[string]json.RawMessage{
		"describe":          schemaDescribe,
		"read":              schemaRead,
		"create":            schemaCreate,
		"update":            schemaUpdate,
		"delete":            schemaDelete,
		"execute":           schemaExecute,
		"aggregate":         schemaAggregate,
		"begin_transaction": schemaBeginTransaction,
		"transaction_token": transactionTokenSchema,
	} {
		var m map[string]any
		if err := json.Unmarshal(s, &m); err != nil {
			t.Errorf("%s schema is invalid JSON: %v", name, err)
			continue
		}
		if m["type"] != "object" {
			t.Errorf("%s schema type = %v, want object", name, m["type"])
		}
	}
}
