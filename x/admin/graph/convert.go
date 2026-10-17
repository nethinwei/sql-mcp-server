package graph

import (
	"context"
	"maps"
	"slices"
	"sort"

	"github.com/99designs/gqlgen/graphql"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// Configuration is the read model of a configuration: its datasources (with
// secrets redacted) and its sections as the workspace holds them, item by
// item (configapp.Sections).
type Configuration struct {
	Datasources []Datasource
	Entities    []Entity
	Roles       []Role
	Users       []User
	Settings    any
}

// toConfiguration builds the read model of what a configuration says (spec)
// and resolves to (cfg, for the datasources).
func toConfiguration(spec *config.Spec, cfg *config.Config) (*Configuration, error) {
	s, err := configapp.SectionsOf(spec)
	if err != nil {
		return nil, err
	}
	return &Configuration{
		Datasources: toDatasources(cfg), Entities: s.Entities, Roles: s.Roles, Users: s.Users, Settings: s.Settings,
	}, nil
}

// onlyDatasources reports that a configuration is read for its datasources
// alone (as the console does with every revision it loads): the other
// sections of a large one take long to convert.
func onlyDatasources(ctx context.Context) bool {
	for _, f := range graphql.CollectAllFields(ctx) {
		if f != "datasources" && f != "__typename" {
			return false
		}
	}
	return true
}

func toDatasources(cfg *config.Config) []Datasource {
	out := make([]Datasource, 0, len(cfg.Databases))
	for name, db := range cfg.Databases {
		connections := db.ConnectionsOrDSN()
		route := db.Route()
		ds := Datasource{
			Name: name, Driver: db.Driver, Dsn: bootstrap.RedactDSN(db.Driver, connections[route.Read].DSN),
			Routing:     &DatasourceRouting{Read: route.Read, Write: route.Write, Execute: route.Execute},
			Connections: make([]DatasourceConnection, 0, len(connections)),
		}
		if db.ReadAfterWrite > 0 {
			ds.ReadAfterWrite = kit.Optional(db.ReadAfterWrite.String())
		}
		if db.SQLEscape.Enabled() {
			ds.SQLEscape = &DatasourceSQLEscape{Connection: db.SQLEscape.Connection}
		}
		for _, connection := range slices.Sorted(maps.Keys(connections)) {
			c := connections[connection]
			role := c.Role
			if role == "" {
				role = "primary"
			}
			ds.Connections = append(ds.Connections, DatasourceConnection{
				Name: connection, Dsn: bootstrap.RedactDSN(db.Driver, c.DSN), Role: role, Pooler: kit.Optional(c.Pooler),
			})
		}
		out = append(out, ds)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
