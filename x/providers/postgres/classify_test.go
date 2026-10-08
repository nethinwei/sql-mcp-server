package postgres

import (
	"slices"
	"testing"
)

func TestDetailColumns(t *testing.T) {
	t.Parallel()
	for detail, want := range map[string][]string{
		`Key (email)=(a@x) already exists.`:                          {"email"},
		`Key (tenant_id, "Code")=(1, x) already exists.`:             {"tenant_id", "Code"},
		`Key (customer_id)=(5) is not present in table "customers".`: {"customer_id"},
		`Failing row contains (1, null).`:                            nil,
	} {
		if got := detailColumns(detail); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", detail, got, want)
		}
	}
}
