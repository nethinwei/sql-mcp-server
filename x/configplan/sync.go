package configplan

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// syncAll syncs the entities of targets and those the serving configuration
// found incomplete, keys first: whether a field can go depends on the keys
// naming it, and what names a field is indexed once (see fieldUses).
func (ev *evaluation) syncAll(targets []target, exclude map[string]bool) {
	targets = append(slices.Clone(targets), ev.runtimeTargets(targets, exclude)...)
	if ev.plan.Sync.Keys {
		ev.syncStructure(targets)
	}
	for _, t := range targets {
		ev.sync(t)
	}
	for _, t := range targets {
		if columns := ev.in.MissingRowColumns[t.id]; len(columns) > 0 {
			ev.except(Exception{Kind: ExceptionRowColumnMissing, Ref: t.row.Ref, Entity: t.id,
				Detail: strings.Join(columns, ", ")})
		}
	}
	if ev.plan.Sync.RemoveTables {
		ev.removeGone(GoneTables(ev.in.Sources, ev.in.Base, ev.plan.Scope, ev.in.Missing), exclude)
	}
}

// runtimeTargets are the entities in scope, but not among targets, that the
// serving configuration found incomplete: their schemas were not scanned
// (a scan, when there is one, says which columns are gone), so the fields
// whose columns it found missing are what they lack.
func (ev *evaluation) runtimeTargets(targets []target, exclude map[string]bool) []target {
	known := map[string]bool{}
	for _, t := range targets {
		known[t.id] = true
	}
	defaults := map[string]string{}
	for _, src := range ev.in.Sources {
		defaults[src.Datasource] = src.Catalog.Default
	}
	ids := slices.Sorted(maps.Keys(ev.in.MissingFields))
	ids = append(ids, slices.Sorted(maps.Keys(ev.in.MissingRowColumns))...)
	m := ev.plan.Scope.compile()
	var out []target
	for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
		i, ok := ev.index[id]
		if !ok || known[id] {
			continue
		}
		e := ev.cfg.Entities[i]
		ds := e.DatasourceName()
		table := entity.Entity{Schema: e.Schema, Source: e.PhysicalSource(), Description: e.Description}
		if table.Schema == "" {
			table.Schema = defaults[ds]
		}
		row := Row{Ref: Ref(ds, table.Schema, table.Source), Datasource: ds, Entity: &e, Status: ev.statusOf(id),
			Missing: configuredFields(e, ev.in.MissingFields[id])}
		if e.Kind == "procedure" || exclude[row.Ref] || m.datasources != nil && !m.datasources[ds] ||
			!m.table(ds, table) || !m.row(row) {
			continue
		}
		out = append(out, target{row: row, id: id})
	}
	return out
}

// statusOf is where a configured entity stands: published or in the
// workspace only.
func (ev *evaluation) statusOf(id string) Status {
	if ev.in.Published != nil && slices.ContainsFunc(ev.in.Published.Entities, func(e config.EntityConfig) bool {
		return e.ID() == id
	}) {
		return StatusPublished
	}
	return StatusWorkspace
}

// configuredFields are those of columns that e configures as fields.
func configuredFields(e config.EntityConfig, columns []string) []string {
	var out []string
	for _, c := range columns {
		if slices.ContainsFunc(e.Fields, func(f config.FieldConfig) bool { return f.Name == c }) {
			out = append(out, c)
		}
	}
	return out
}

// syncStructure brings the keys and kinds of the scanned targets in line
// with their tables and reports how their relationships and the foreign
// keys differ. The foreign keys are read as an import would read them, once
// per datasource.
func (ev *evaluation) syncStructure(targets []target) {
	byDatasource := map[string][]target{}
	for _, t := range targets {
		if !t.new && t.row.Table.Source != "" {
			byDatasource[t.row.Datasource] = append(byDatasource[t.row.Datasource], t)
		}
	}
	for _, src := range ev.in.Sources {
		ts := byDatasource[src.Datasource]
		if len(ts) == 0 {
			continue
		}
		only := make(map[string]bool, len(ts))
		for _, t := range ts {
			only[TableKey(t.row.Table.Schema, t.row.Table.Source)] = true
		}
		imported := map[string]config.EntityConfig{}
		for _, c := range Candidates(src.Datasource, src.Catalog, ev.in.Base, nil, only) {
			imported[TableKey(c.Table.Schema, c.Table.Source)] = c.Entity
		}
		for _, t := range ts {
			ev.syncKind(t)
			ev.syncKeys(t)
			ev.reportRelationships(t, imported[TableKey(t.row.Table.Schema, t.row.Table.Source)])
		}
	}
}

func (ev *evaluation) syncKind(t target) {
	kind := ""
	if t.row.Table.Kind == entity.KindView {
		kind = "view"
	}
	if ev.entity(t).Kind != kind {
		ev.change(t).Kind = kind
		ev.summarize(Change{Kind: ChangeSyncKind}, t.id, 0)
	}
}

// syncKeys sets a table's keys to those scanned. A view keeps the keys
// configured on it: it has none in the database, and the declared ones are
// the administrator's.
func (ev *evaluation) syncKeys(t target) {
	if t.row.Table.Kind == entity.KindView {
		return
	}
	e := ev.entity(t)
	primary, unique := t.row.Table.PrimaryKey(), IdentityUniqueKeys(t.row.Table)
	if slices.Equal(e.PrimaryKey, primary) && sameKeys(e.UniqueKeys, unique) {
		return
	}
	before := keyColumns(e.PrimaryKey, e.UniqueKeys)
	e = ev.change(t)
	e.PrimaryKey, e.UniqueKeys = slices.Clone(primary), unique
	if len(unique) == 0 {
		e.UniqueKeys = nil
	}
	ev.summarize(Change{Kind: ChangeSyncKeys}, t.id, 0)
	if len(primary) == 0 && len(unique) == 0 {
		ev.except(Exception{Kind: ExceptionNoKey, Ref: t.row.Ref, Entity: t.id, Detail: "update, delete"})
	}
	after := keyColumns(e.PrimaryKey, e.UniqueKeys)
	for _, column := range before {
		if slices.Contains(after, column) {
			continue
		}
		for _, where := range ev.joinsOn(t.id, column) {
			ev.except(Exception{Kind: ExceptionKeyReferenced, Ref: t.row.Ref, Entity: t.id,
				Detail: column + ": " + where})
		}
	}
}

func keyColumns(primary []string, unique [][]string) []string {
	out := slices.Clone(primary)
	for _, k := range unique {
		out = append(out, k...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// sameKeys compares unique keys as sets of column lists.
func sameKeys(a, b [][]string) bool {
	norm := func(ks [][]string) []string {
		out := make([]string, len(ks))
		for i, k := range ks {
			out[i] = strings.Join(k, "\x00")
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(norm(a), norm(b))
}

// joinsOn lists the relationships joining on a column of the entity id, on
// either side.
func (ev *evaluation) joinsOn(id, column string) []string {
	var out []string
	for _, e := range ev.cfg.Entities {
		for _, r := range e.Relationships {
			target, ok := ev.resolve(r.Target)
			for local, remote := range r.JoinOn {
				if e.ID() == id && local == column || ok && target == id && remote == column {
					out = append(out, "relationship "+e.ID()+"."+r.Name)
					break
				}
			}
		}
	}
	return out
}

// reportRelationships reports the foreign keys no relationship of the
// target follows (to entities that exist), and its relationships named as
// an import names them that no foreign key backs. Neither is changed: a
// configured relationship is not known to come from a foreign key.
func (ev *evaluation) reportRelationships(t target, imported config.EntityConfig) {
	e := ev.entity(t)
	for _, r := range imported.Relationships {
		if _, ok := ev.index[r.Target]; !ok || slices.ContainsFunc(e.Relationships, ev.sameJoin(r)) {
			continue
		}
		ev.except(Exception{Kind: ExceptionRelationshipNew, Ref: t.row.Ref, Entity: t.id, Detail: describeJoin(r)})
	}
	for _, r := range e.Relationships {
		target, ok := ev.resolve(r.Target)
		if !ok || !importedName(r.Name, ev.cfg.Entities[ev.index[target]].Name) ||
			slices.ContainsFunc(imported.Relationships, ev.sameJoin(r)) {
			continue
		}
		ev.except(Exception{Kind: ExceptionRelationshipStale, Ref: t.row.Ref, Entity: t.id, Detail: r.Name})
	}
}

// sameJoin matches relationships of one target, cardinality and join.
func (ev *evaluation) sameJoin(r config.RelationshipConfig) func(config.RelationshipConfig) bool {
	want, _ := ev.resolve(r.Target)
	return func(o config.RelationshipConfig) bool {
		got, ok := ev.resolve(o.Target)
		return ok && got == want && o.Cardinality == r.Cardinality && maps.Equal(o.JoinOn, r.JoinOn)
	}
}

// importedName reports a relationship name an import gives a relationship
// to target: its name, numbered past the first (see uniqueRelationshipName).
func importedName(name, target string) bool {
	if name == target {
		return true
	}
	rest, ok := strings.CutPrefix(name, target+"_")
	var n int
	_, err := fmt.Sscanf(rest, "%d", &n)
	return ok && err == nil && n >= 2 && fmt.Sprint(n) == rest
}

func describeJoin(r config.RelationshipConfig) string {
	pairs := make([]string, 0, len(r.JoinOn))
	for _, local := range slices.Sorted(maps.Keys(r.JoinOn)) {
		pairs = append(pairs, local+" = "+r.JoinOn[local])
	}
	return r.Cardinality + " " + r.Target + " (" + strings.Join(pairs, ", ") + ")"
}
