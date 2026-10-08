package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// physicalOf identifies the server behind a datasource. A server that cannot
// be identified is keyed by the datasource name, so only entities of that
// datasource can be recognized as exposing the same relation.
func physicalOf(ctx context.Context, datasource string, in introspect.Introspector) (introspect.Physical, error) {
	var physical introspect.Physical
	if namer, ok := in.(introspect.PhysicalNamer); ok {
		var err error
		if physical, err = namer.Physical(ctx); err != nil {
			return physical, err
		}
	}
	if physical.Server == "" {
		physical.Server = "datasource:" + datasource
	}
	return physical, nil
}

// identifyRelations sets each table or view entity's relation key and, when
// the catalog is known, takes its kind from the database (a configured table
// may be a view) and resolves its default schema.
func identifyRelations(
	entities []entity.Entity,
	physical introspect.Physical,
	cat *introspect.Catalog,
) []entity.Entity {
	for i, e := range entities {
		if e.Kind == entity.KindProcedure {
			continue
		}
		schema := e.Schema
		if cat != nil {
			if found, ok := cat.Lookup(e.Schema, e.Source); ok {
				schema = found.Schema
				entities[i].Kind, entities[i].Derived = found.Kind, found.Derived
				entities[i].Keys = databaseKeys(e, found)
				entities[i].SideEffects = found.SideEffects
				entities[i].Cascades = slices.Clone(found.Cascades)
				for j, c := range entities[i].Cascades {
					entities[i].Cascades[j].Relation = physical.RelationKey(c.Schema, c.Table)
				}
			}
		}
		entities[i].Relation = physical.RelationKey(schema, e.Source)
	}
	return entities
}

// databaseKeys returns the keys of an entity: a table's own keys, which the
// database enforces and which replace the configured ones (a configured key
// the database lacks is reported); a derived relation has none, so its
// configured keys are declared, serving reads only (I-5).
func databaseKeys(configured, found entity.Entity) []entity.Key {
	if found.Derived {
		keys := slices.Clone(configured.Keys)
		for i := range keys {
			keys[i].Declared = true
		}
		return keys
	}
	for _, want := range configured.Keys {
		if !slices.ContainsFunc(found.Keys, func(k entity.Key) bool { return slices.Equal(k.Columns, want.Columns) }) {
			slog.Warn("configured key is not a key of the table; the table's keys apply",
				"entity", configured.Name, "key", want.Columns)
		}
	}
	return found.Keys
}

// checkUniqueRelations enforces that at most one entity exposes a relation
// (I-1), across datasources that reach the same server, and links each
// cascade to the entity exposing the relation it writes.
func checkUniqueRelations(entities []entity.Entity) error {
	owner := make(map[string]string, len(entities))
	for _, e := range entities {
		if e.Kind == entity.KindProcedure {
			continue
		}
		if other, taken := owner[e.Relation]; taken {
			return fmt.Errorf("entities %q and %q expose the same relation %s; keep one entity "+
				"(use several connections for different accounts)", other, e.Name, e.Source)
		}
		owner[e.Relation] = e.Name
	}
	for i := range entities {
		for j, c := range entities[i].Cascades {
			entities[i].Cascades[j].Entity = owner[c.Relation]
		}
	}
	return nil
}
