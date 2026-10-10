package configapp

import (
	"strings"
	"testing"
)

// A tenant policy that is not an object is rejected rather than dropped,
// which would silently remove the tenant constraint.
func TestEntityItemRejectsNonObjectTenantPolicy(t *testing.T) {
	t.Parallel()
	for _, policy := range []any{"tenant_id", []any{"tenant_id"}} {
		_, err := EntityItem{Name: "orders", TenantPolicy: policy}.spec()
		if err == nil || !strings.Contains(err.Error(), "tenantPolicy must be an object") {
			t.Errorf("tenantPolicy %#v: err = %v", policy, err)
		}
	}
	e, err := EntityItem{Name: "orders", TenantPolicy: map[string]any{"column": "tenant_id"}}.spec()
	if err != nil || e.TenantPolicy["column"] != "tenant_id" {
		t.Fatalf("object tenantPolicy = %v, %v", e.TenantPolicy, err)
	}
}
