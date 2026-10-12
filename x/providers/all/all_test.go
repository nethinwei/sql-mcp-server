package all

import (
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

func TestBuiltInDriversRegistered(t *testing.T) {
	for _, want := range []string{"mysql", "oceanbase", "postgres"} {
		if !providerregistry.IsRegistered(want) {
			t.Errorf("driver %q is not registered", want)
		}
	}
	// Every built-in driver states what its connections guarantee, so a
	// configuration can be judged before it is applied.
	for _, driver := range providerregistry.Drivers() {
		if !providerregistry.HasLimits(driver) {
			t.Errorf("driver %q declares no limits", driver)
		}
	}
}
