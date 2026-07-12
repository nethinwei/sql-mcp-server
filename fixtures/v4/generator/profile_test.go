package workload

import "testing"

func TestDiagnosticProfileAddsIsolatedTenantEntity(t *testing.T) {
	dataset := Generate(DefaultConfig())
	defaultCount := len(dataset.Profile()["entities"].([]any))
	if defaultCount != len(dataset.Tables) {
		t.Fatalf("default entity count = %d, want %d", defaultCount, len(dataset.Tables))
	}
	entities := dataset.DiagnosticProfile()["entities"].([]any)
	if len(entities) != defaultCount+2 {
		t.Fatalf("diagnostic entity count = %d, want %d", len(entities), defaultCount+2)
	}
	tenant := entities[len(entities)-2].(map[string]any)
	if tenant["name"] != "tenant_customers" || tenant["source"] != "wl_customers" {
		t.Fatalf("tenant entity = %#v", tenant)
	}
	policies := tenant["rowPolicies"].(map[string]any)
	analyst := policies["analyst"].(map[string]any)
	if analyst["field"] != "organization_id" || analyst["value"] != 1 {
		t.Fatalf("analyst row policy = %#v", analyst)
	}
	restricted := entities[len(entities)-1].(map[string]any)
	if restricted["name"] != "internal_audit_log" {
		t.Fatalf("restricted entity = %#v", restricted)
	}
}
