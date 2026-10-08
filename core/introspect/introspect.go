package introspect

import (
	"context"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// Introspector discovers entity metadata (tables, columns, keys, procedure
// parameters) from a live database. Implementations live in x/providers.
type Introspector interface {
	Discover(ctx context.Context, sources []string) ([]entity.Entity, error)
}

// SchemaLister is implemented by introspectors that can name the schemas of
// their database: all user schemas, and the default one where unqualified
// table names resolve ("" when there is none). Discover with no schemas scans
// the default one.
type SchemaLister interface {
	Schemas(ctx context.Context) (all []string, current string, err error)
}

// Catalog is a set of discovered tables and the schema unqualified table names
// resolve to. Every lookup of the table an entity reads goes through it, so
// drift checks, comment defaults and imports agree with the generated SQL.
type Catalog struct {
	Tables []entity.Entity
	// Default is where an entity without a schema reads; when unknown, such
	// an entity matches a table name unique in the catalog.
	Default string
}

// Lookup finds the table named by schema (empty for the default) and table.
func (c Catalog) Lookup(schema, table string) (entity.Entity, bool) {
	if schema == "" {
		schema = c.Default
	}
	var found []entity.Entity
	for _, t := range c.Tables {
		if tableName(t) != table {
			continue
		}
		if schema != "" && t.Schema == schema {
			return t, true
		}
		found = append(found, t)
	}
	if schema == "" && len(found) == 1 {
		return found[0], true
	}
	return entity.Entity{}, false
}

// LoadCatalog discovers the named schemas and, with withDefault, the default
// one, and records which schema is the default.
func LoadCatalog(ctx context.Context, in Introspector, schemas []string, withDefault bool) (Catalog, error) {
	var cat Catalog
	lister, ok := in.(SchemaLister)
	if !ok {
		// The default schema is unknown: discover it on its own, and match
		// unqualified names by unique name.
		return loadWithoutDefault(ctx, in, schemas, withDefault)
	}
	_, current, err := lister.Schemas(ctx)
	if err != nil {
		return cat, err
	}
	cat.Default = current
	if withDefault && current != "" && !slices.Contains(schemas, current) {
		schemas = append(slices.Clone(schemas), current)
	}
	if len(schemas) == 0 {
		return cat, nil
	}
	cat.Tables, err = in.Discover(ctx, schemas)
	return cat, err
}

func loadWithoutDefault(ctx context.Context, in Introspector, schemas []string, withDefault bool) (Catalog, error) {
	var cat Catalog
	if len(schemas) > 0 {
		tables, err := in.Discover(ctx, schemas)
		if err != nil {
			return cat, err
		}
		cat.Tables = tables
	}
	if withDefault {
		tables, err := in.Discover(ctx, nil)
		if err != nil {
			return cat, err
		}
		cat.Tables = append(cat.Tables, tables...)
	}
	return cat, nil
}

// Drift describes differences between configured and discovered schemas.
type Drift struct {
	// Missing lists configured entities/fields absent from the DB
	// (entity name, or "entity.field").
	Missing []string
	// Extra lists DB columns not present in the config.
	Extra []string
	// TypeChanged lists fields whose DB type differs from the config.
	TypeChanged []string
}

// Reconcile matches configured table and view entities to their tables in the
// catalog. It returns the entities with empty entity and field descriptions
// taken from the database comments (a configured description overrides a
// comment), and the drift: missing entities by name, missing and extra fields
// as "entity.field". Procedures are returned unchanged. It is pure and
// deterministic.
func Reconcile(configured []entity.Entity, c Catalog) ([]entity.Entity, Drift) {
	out := make([]entity.Entity, len(configured))
	d := Drift{}
	for i, ce := range configured {
		out[i] = ce
		if ce.Kind == entity.KindProcedure {
			continue
		}
		physical := ce.Source
		if physical == "" {
			physical = ce.Name
		}
		de, ok := c.Lookup(ce.Schema, physical)
		if !ok {
			d.Missing = append(d.Missing, ce.Name)
			continue
		}
		out[i] = inherit(ce, de)
		columns := indexAttrs(de.Attributes)
		for _, a := range ce.Attributes {
			if _, ok := columns[a.Name]; !ok {
				d.Missing = append(d.Missing, ce.Name+"."+a.Name)
			}
		}
		fields := indexAttrs(ce.Attributes)
		for _, a := range de.Attributes {
			if _, ok := fields[a.Name]; !ok {
				d.Extra = append(d.Extra, ce.Name+"."+a.Name)
			}
		}
	}
	return out, d
}

// tableName is a discovered table's physical name.
func tableName(de entity.Entity) string {
	if de.Source != "" {
		return de.Source
	}
	return de.Name
}

func indexAttrs(as []entity.Attribute) map[string]struct{} {
	m := make(map[string]struct{}, len(as))
	for _, a := range as {
		m[a.Name] = struct{}{}
	}
	return m
}

func inherit(ce, de entity.Entity) entity.Entity {
	if ce.Description == "" {
		ce.Description = de.Description
	}
	comments := make(map[string]string, len(de.Attributes))
	for _, a := range de.Attributes {
		comments[a.Name] = a.Description
	}
	attrs := make([]entity.Attribute, len(ce.Attributes))
	for i, a := range ce.Attributes {
		if a.Description == "" {
			a.Description = comments[a.Name]
		}
		attrs[i] = a
	}
	ce.Attributes = attrs
	return ce
}
