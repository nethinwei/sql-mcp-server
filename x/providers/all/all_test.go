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
}
