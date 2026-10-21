package configplan

import (
	"cmp"
	"maps"
	"slices"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// Candidate is a scanned table with the entity that reads it, if any, and a
// zero-permission entity proposed for it.
type Candidate struct {
	Table entity.Entity
	// Configured is the entity of the configuration that reads the table.
	Configured *config.EntityConfig
	// Entity is an entity proposed for the table, named like the
	// configured one if any.
	Entity config.EntityConfig
	// ID identifies the entity: the configured one's when configured.
	ID string
	// Dropped are the targets of the relationships Entity leaves out: those
	// of foreign keys on columns a configured entity at either end has no
	// field for (a join names fields).
	Dropped []string
}

// TableKey identifies a table within a datasource: tables of different
// schemas may share a name.
func TableKey(schema, table string) string { return schema + "." + table }

// ConfiguredTables maps each catalog table (by TableKey) to the configured
// entity of datasource that reads it, resolved by the catalog like the runtime
// does.
func ConfiguredTables(datasource string, cat introspect.Catalog, cfg *config.Config) map[string]config.EntityConfig {
	out := map[string]config.EntityConfig{}
	for _, e := range cfg.Entities {
		if e.DatasourceName() != datasource || e.Kind == "procedure" {
			continue
		}
		if d, ok := cat.Lookup(e.Schema, e.PhysicalSource()); ok {
			out[TableKey(d.Schema, d.Source)] = e
		}
	}
	return out
}

// Candidates proposes an entity for the tables of cat (those of only, by
// TableKey, when only is not nil), sorted by schema and table: names follow
// the tables, numbered when another entity of the datasource and schema uses
// one; names (by TableKey) are preferred where given. Names are worked out
// over every table, so a candidate is named the same whichever are asked
// for. Foreign keys become relationships between candidates and to
// configured entities, in both directions.
func Candidates(
	datasource string, cat introspect.Catalog, cfg *config.Config, names map[string]string, only map[string]bool,
) []Candidate {
	configured := ConfiguredTables(datasource, cat, cfg)
	taken := map[string]bool{}
	for _, e := range cfg.Entities {
		if e.DatasourceName() == datasource {
			taken[TableKey(cmp.Or(e.Schema, cat.Default), e.Name)] = true
		}
	}
	chosen := candidateNames(datasource, cat.Tables, configured, taken, names)
	// A foreign key may reference a table of a schema not scanned: it is
	// related when an entity is configured on it.
	for _, e := range cfg.Entities {
		key := TableKey(cmp.Or(e.Schema, cat.Default), e.PhysicalSource())
		if _, ok := chosen[key]; !ok && e.DatasourceName() == datasource && e.Kind != "procedure" {
			chosen[key] = configuredName(e)
		}
	}
	out := make([]Candidate, 0, len(cat.Tables))
	for _, d := range cat.Tables {
		key := TableKey(d.Schema, d.Source)
		if only != nil && !only[key] {
			continue
		}
		c := Candidate{Table: d, ID: chosen[key].id}
		c.Entity, c.Dropped = candidateEntity(datasource, d, chosen)
		if e, ok := configured[key]; ok {
			c.Configured = &e
		}
		out = append(out, c)
	}
	addReverseRelationships(out, cat.Tables, chosen)
	slices.SortFunc(out, func(a, b Candidate) int {
		return cmp.Or(cmp.Compare(a.Table.Schema, b.Table.Schema), cmp.Compare(a.Table.Source, b.Table.Source))
	})
	return out
}

// candidateName names the entity of a table; fields are those of a
// configured one (nil: a candidate's, every column).
type candidateName struct {
	name, id string
	fields   map[string]bool
}

func configuredName(e config.EntityConfig) candidateName {
	fields := make(map[string]bool, len(e.Fields))
	for _, f := range e.Fields {
		fields[f.Name] = true
	}
	return candidateName{name: e.Name, id: e.ID(), fields: fields}
}

// has reports whether the entity has a field for each of columns.
func (n candidateName) has(columns []string) bool {
	return n.fields == nil || !slices.ContainsFunc(columns, func(c string) bool { return !n.fields[c] })
}

func candidateNames(
	datasource string, discovered []entity.Entity, configured map[string]config.EntityConfig,
	taken map[string]bool, preferred map[string]string,
) map[string]candidateName {
	out := make(map[string]candidateName, len(discovered))
	used := maps.Clone(taken)
	for _, d := range discovered {
		if e, ok := configured[TableKey(d.Schema, d.Source)]; ok {
			out[TableKey(d.Schema, d.Source)] = configuredName(e)
		}
	}
	// Preferred names go first, so numbering moves other tables aside.
	for _, pass := range []bool{true, false} {
		for _, d := range discovered {
			key := TableKey(d.Schema, d.Source)
			if _, done := out[key]; done {
				continue
			}
			if _, ok := preferred[key]; ok != pass {
				continue
			}
			out[key] = nextName(datasource, d, preferred[key], used)
		}
	}
	return out
}

// nextName names the candidate of d: preferred or the table name, numbered
// past the names used, which it then uses.
func nextName(datasource string, d entity.Entity, preferred string, used map[string]bool) candidateName {
	base := cmp.Or(preferred, d.Source)
	name := base
	for n := 2; used[TableKey(d.Schema, name)]; n++ {
		name = base + "_" + strconv.Itoa(n)
	}
	used[TableKey(d.Schema, name)] = true
	return candidateName{name: name, id: entity.ID(datasource, d.Schema, name)}
}

// IdentityUniqueKeys lists the unique keys of d that identify a row.
func IdentityUniqueKeys(d entity.Entity) [][]string {
	out := [][]string{}
	for _, k := range d.Keys {
		if !k.Primary && k.Reason == "" {
			out = append(out, k.Columns)
		}
	}
	return out
}

// candidateEntity proposes an entity with every column, the primary key and a
// belongs-to relationship per foreign key. Table and column comments are not
// copied: at runtime they are the default descriptions, so later comment
// changes still apply and only a written description overrides. It carries no
// roles, grants or row policies: nobody can access it until an administrator
// grants access explicitly. It returns the targets of the foreign keys it
// leaves out (see Candidate.Dropped).
func candidateEntity(
	datasource string, d entity.Entity, names map[string]candidateName,
) (config.EntityConfig, []string) {
	self := names[TableKey(d.Schema, d.Source)]
	out := config.EntityConfig{
		Name: self.name, Source: d.Source, DataSource: datasource, Schema: d.Schema,
		PrimaryKey: d.PrimaryKey(), UniqueKeys: IdentityUniqueKeys(d),
		Fields: make([]config.FieldConfig, 0, len(d.Attributes)),
		// As in effect: DML tools on by default, not written (config.SpecOf).
		MCP: config.MCPFlags{DMLTools: true},
	}
	if d.Kind == entity.KindView {
		out.Kind = "view"
	}
	for _, a := range d.Attributes {
		out.Fields = append(out.Fields, config.FieldConfig{Name: a.Name})
	}
	var dropped []string
	for _, fk := range d.ForeignKeys {
		target, ok := names[TableKey(cmp.Or(fk.RefSchema, d.Schema), fk.RefRelation)]
		if !ok {
			continue
		}
		if !target.has(fk.RefColumns) {
			dropped = append(dropped, target.id)
			continue
		}
		joinOn := make(map[string]string, len(fk.Columns))
		for i, column := range fk.Columns {
			joinOn[column] = fk.RefColumns[i]
		}
		out.Relationships = append(out.Relationships, config.RelationshipConfig{
			Name: uniqueRelationshipName(out.Relationships, target.name), Target: target.id, Cardinality: "belongs-to",
			JoinOn: joinOn,
		})
	}
	return out, dropped
}

// addReverseRelationships adds a has-many relationship on each candidate for
// every foreign key of a table of the catalog referencing it (the
// belongs-to relationship of that table's candidate), whether or not that
// table's candidate was built; not when that table's configured entity has
// no field for a column of the foreign key.
func addReverseRelationships(cs []Candidate, tables []entity.Entity, names map[string]candidateName) {
	byID := make(map[string]*Candidate, len(cs))
	for i := range cs {
		byID[cs[i].ID] = &cs[i]
	}
	for _, d := range tables {
		child := names[TableKey(d.Schema, d.Source)]
		for _, fk := range d.ForeignKeys {
			target, ok := names[TableKey(cmp.Or(fk.RefSchema, d.Schema), fk.RefRelation)]
			if !ok {
				continue
			}
			c, ok := byID[target.id]
			if !ok {
				continue
			}
			if !child.has(fk.Columns) {
				c.Dropped = append(c.Dropped, child.id)
				continue
			}
			parent := &c.Entity
			joinOn := make(map[string]string, len(fk.Columns))
			for i, column := range fk.Columns {
				joinOn[fk.RefColumns[i]] = column
			}
			parent.Relationships = append(parent.Relationships, config.RelationshipConfig{
				Name: uniqueRelationshipName(parent.Relationships, child.name), Target: child.id,
				Cardinality: "has-many", JoinOn: joinOn,
			})
		}
	}
}

func uniqueRelationshipName(existing []config.RelationshipConfig, base string) string {
	if config.RelationshipNameReserved(base) { // a table named x1
		base = "to_" + base
	}
	name := base
	for n := 2; slices.ContainsFunc(existing, func(r config.RelationshipConfig) bool { return r.Name == name }); n++ {
		name = base + "_" + strconv.Itoa(n)
	}
	return name
}
