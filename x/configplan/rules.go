package configplan

import (
	"cmp"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

// importTables adds an entity for each new table when the plan imports, and
// returns every entity the plan applies to: those added and those of the
// configured tables in scope.
func (ev *evaluation) importTables(rows []Row) []target {
	var out []target
	byDatasource := map[string][]Row{}
	for _, r := range rows {
		if r.Entity != nil {
			out = append(out, target{row: r, id: r.Entity.ID()})
		} else if ev.plan.Import {
			byDatasource[r.Datasource] = append(byDatasource[r.Datasource], r)
		}
	}
	for _, src := range ev.in.Sources {
		if news := byDatasource[src.Datasource]; len(news) > 0 {
			out = append(out, ev.importFrom(src, news)...)
		}
	}
	ev.dropDanglingRelationships()
	return out
}

func (ev *evaluation) importFrom(src Source, rows []Row) []target {
	names := map[string]string{}
	for _, r := range rows {
		if name, ok := ev.plan.Names[r.Ref]; ok {
			names[TableKey(r.Table.Schema, r.Table.Source)] = name
		}
	}
	byKey := map[string]Candidate{}
	only := make(map[string]bool, len(rows))
	for _, r := range rows {
		only[TableKey(r.Table.Schema, r.Table.Source)] = true
	}
	for _, c := range Candidates(src.Datasource, src.Catalog, ev.in.Base, names, only) {
		byKey[TableKey(c.Table.Schema, c.Table.Source)] = c
	}
	out := make([]target, 0, len(rows))
	for _, r := range rows {
		c := byKey[TableKey(r.Table.Schema, r.Table.Source)]
		e := c.Entity
		ev.index[e.ID()] = len(ev.cfg.Entities)
		ev.cfg.Entities = append(ev.cfg.Entities, e)
		ev.added = append(ev.added, e.ID())
		ev.imported[r.Ref] = e.ID()
		ev.changed[e.ID()] = true
		ev.summarize(Change{Kind: ChangeImport}, e.ID(), 0)
		if e.Name != cmp.Or(ev.plan.Names[r.Ref], r.Table.Source) {
			ev.except(Exception{Kind: ExceptionRenamed, Ref: r.Ref, Entity: e.ID(), Detail: e.Name})
		}
		if len(e.PrimaryKey) == 0 && len(e.UniqueKeys) == 0 {
			ev.except(Exception{Kind: ExceptionNoKey, Ref: r.Ref, Entity: e.ID()})
		}
		if r.Table.SideEffects {
			ev.except(Exception{Kind: ExceptionSideEffects, Ref: r.Ref, Entity: e.ID()})
		}
		out = append(out, target{row: r, id: e.ID(), new: true})
	}
	return out
}

// dropDanglingRelationships drops the relationships of added entities to
// tables that have no entity.
func (ev *evaluation) dropDanglingRelationships() {
	for _, id := range ev.added {
		e := &ev.cfg.Entities[ev.index[id]]
		e.Relationships = slices.DeleteFunc(e.Relationships, func(r config.RelationshipConfig) bool {
			_, ok := ev.index[r.Target]
			if !ok {
				ev.except(Exception{Kind: ExceptionRelationshipDropped, Entity: id, Detail: r.Target})
			}
			return !ok
		})
	}
}

// columnType is the type of a column of the table, "" when none.
func columnType(r Row, column string) string {
	for _, a := range r.Table.Attributes {
		if a.Name == column {
			return a.Domain.Type
		}
	}
	return ""
}

// applyFields applies the field rules, in order, to an entity.
func (ev *evaluation) applyFields(t target) {
	for _, rule := range ev.fieldRules {
		e := ev.entity(t)
		for i, f := range e.Fields {
			if !rule.columns.match(f.Name) || rule.types.set() && !rule.types.match(columnType(t.row, f.Name)) {
				continue
			}
			if rule.Exclude && !f.Exclude {
				ev.change(t).Fields[i].Exclude = true
				ev.summarize(Change{Kind: ChangeExclude}, t.id, 1)
			}
			if rule.Mask != "" && f.Mask != rule.Mask {
				ev.change(t).Fields[i].Mask = rule.Mask
				ev.summarize(Change{Kind: ChangeMask, Mask: rule.Mask}, t.id, 1)
			}
		}
	}
}

// sync adds fields for new columns and removes the fields of dropped columns
// that nothing else references; grants' field lists follow.
func (ev *evaluation) sync(t target) {
	if t.new {
		return
	}
	rule := ev.plan.Sync
	if rule.Add && len(t.row.Added) > 0 {
		e := ev.change(t)
		for _, name := range t.row.Added {
			e.Fields = append(e.Fields, config.FieldConfig{Name: name, Exclude: rule.HideNew})
		}
		ev.summarize(Change{Kind: ChangeSyncAdd}, t.id, len(t.row.Added))
	}
	if !rule.Remove || len(t.row.Missing) == 0 {
		return
	}
	for _, field := range t.row.Missing {
		if where := ev.fieldReferences(t.id, field); len(where) > 0 {
			ev.except(Exception{Kind: ExceptionReferenced, Ref: t.row.Ref, Entity: t.id,
				Detail: field + ": " + strings.Join(where, ", ")})
			continue
		}
		e := ev.change(t)
		e.Fields = slices.DeleteFunc(e.Fields, func(f config.FieldConfig) bool { return f.Name == field })
		ev.dropGrantField(t.id, field)
		ev.summarize(Change{Kind: ChangeSyncRemove}, t.id, 1)
	}
}
