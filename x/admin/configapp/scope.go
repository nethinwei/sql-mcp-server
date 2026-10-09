package configapp

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// A principal limited to some datasources (an MCP token's scope) reads and
// plans within them: a scope naming no datasource takes the principal's,
// and one naming others is refused, so what is matched, imported, granted or
// synced never reaches another datasource.

// within narrows scope to p's datasources.
func within(p auth.Principal, scope *configplan.Scope) error {
	if !p.Limited() {
		return nil
	}
	if len(scope.Datasources) == 0 {
		scope.Datasources = slices.Clone(p.Datasources)
		if len(scope.Datasources) == 0 {
			return fmt.Errorf("%w: the token reaches no datasource", auth.ErrOutOfScope)
		}
		return nil
	}
	for _, ds := range scope.Datasources {
		if !p.Allows(ds) {
			return fmt.Errorf("%w: %q", auth.ErrOutOfScope, ds)
		}
	}
	return nil
}

// withinFilter narrows a catalog filter as within does a scope.
func withinFilter(p auth.Principal, f *CatalogFilter) error {
	scope := configplan.Scope{Datasources: f.Datasources}
	if err := within(p, &scope); err != nil {
		return err
	}
	f.Datasources = scope.Datasources
	return nil
}

// refDatasource is the datasource of a table ref (datasource.schema.table).
func refDatasource(ref string) string {
	ds, _, _ := strings.Cut(ref, ".")
	return ds
}
