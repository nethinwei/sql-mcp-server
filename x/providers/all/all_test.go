package all

import (
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

func TestBuiltInDriversRegistered(t *testing.T) {
	drivers := providerregistry.KnownDrivers()
	for _, want := range []string{"mysql", "oceanbase", "postgres"} {
		if !slices.Contains(drivers, want) {
			t.Errorf("KnownDrivers() = %v, missing %q", drivers, want)
		}
	}
}
