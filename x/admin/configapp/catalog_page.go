package configapp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// Catalog pages filter every scanned table on each request, and several
// administrators page through the same filters. Where each table stands is
// worked out once per scans, base configuration and published revision (an
// index: a new scan, an edit of the draft or a publish makes another), and
// the rows of a filter (but for the status, which only narrows them) are kept
// as their positions in it. Both caches merge concurrent misses of a key and
// evict the least recently used entries past a memory budget.

const (
	// indexBudget and matchBudget bound the bytes the indexes and the kept
	// filters take.
	indexBudget = 128 << 20
	matchBudget = 32 << 20
	// rowBytes is about what an indexed row takes.
	rowBytes = 512
)

// indexOf is the index of pc's scans and configurations.
func (s *Scans) indexOf(pc planContext) *configplan.Index {
	key := fmt.Sprintf("%s|%s|%d", pc.fingerprint, pc.baseHash, pc.publishedID)
	return s.indexes.get(key, func() (*configplan.Index, int) {
		x := configplan.NewIndex(pc.sources, pc.base, pc.published)
		return x, x.Len() * rowBytes
	})
}

func scopeOf(f CatalogFilter) configplan.Scope {
	s := configplan.Scope{
		Datasources: f.Datasources, Schemas: f.Schemas, Tables: f.Tables, Search: deref(f.Search), Drift: derefBool(f.Drift),
	}
	for _, st := range f.Status {
		s.Status = append(s.Status, configplan.Status(strings.ToLower(string(st))))
	}
	return s
}

// catalogRows lists the positions of the tables matching f in the index
// they come from, with the counts by status of those matching it but for the
// status.
func (s *Scans) catalogRows(pc planContext, f CatalogFilter) ([]int32, CatalogCounts, *configplan.Index) {
	x := s.indexOf(pc)
	scope := scopeOf(f)
	wide := scope
	wide.Status, wide.Drift = nil, false
	key, _ := json.Marshal(wide)
	matched := s.matches.get(fmt.Sprintf("%s|%s|%d|%s", pc.fingerprint, pc.baseHash, pc.publishedID, key),
		func() ([]int32, int) {
			m := x.Match(wide)
			return m, 4 * len(m)
		})
	var counts CatalogCounts
	var out []int32
	for _, i := range matched {
		row := x.At(i)
		counts.All++
		switch row.Status {
		case configplan.StatusNew:
			counts.New++
		case configplan.StatusWorkspace:
			counts.Workspace++
		case configplan.StatusPublished:
			counts.Published++
		}
		if row.Drift() {
			counts.Drift++
		}
		if (len(scope.Status) == 0 || slices.Contains(scope.Status, row.Status)) && (!scope.Drift || row.Drift()) {
			out = append(out, i)
		}
	}
	return out, counts, x
}

// Catalog pages through the scanned tables matching f, against the
// configuration against is.
func (r *Service) Catalog(
	ctx context.Context, f CatalogFilter, against Against, offset, limit int,
) (*CatalogPage, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	if err := withinFilter(who, &f); err != nil {
		return nil, err
	}
	pc, err := r.planContextOf(ctx, against)
	if err != nil {
		return nil, err
	}
	rows, counts, x := r.Scans.catalogRows(pc, f)
	out := &CatalogPage{Total: len(rows), Counts: &counts, Coverage: coverage(pc.sources, who)}
	offset = min(max(offset, 0), len(rows))
	page := rows[offset:min(offset+max(min(limit, 500), 0), len(rows))]
	out.Tables = make([]CatalogRow, 0, len(page))
	for _, i := range page {
		row := x.At(i)
		out.Tables = append(out.Tables, toCatalogRow(row, x.Referenced(row.Ref)))
	}
	return out, nil
}

func refSchema(t entity.Entity, fk entity.ForeignKey) string {
	if fk.RefSchema == "" {
		return t.Schema
	}
	return fk.RefSchema
}

// CatalogRefs lists the refs of every table matching f.
func (r *Service) CatalogRefs(ctx context.Context, f CatalogFilter, against Against) ([]string, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	if err := withinFilter(who, &f); err != nil {
		return nil, err
	}
	pc, err := r.planContextOf(ctx, against)
	if err != nil {
		return nil, err
	}
	rows, _, x := r.Scans.catalogRows(pc, f)
	out := make([]string, 0, len(rows))
	for _, i := range rows {
		out = append(out, x.At(i).Ref)
	}
	return out, nil
}

// coverage reports what is scanned of each datasource who may read.
func coverage(sources []configplan.Source, who auth.Principal) []CatalogCoverage {
	out := make([]CatalogCoverage, 0, len(sources))
	unscanned := configplan.Unscanned(sources, configplan.Scope{})
	for _, src := range sources {
		if !who.Allows(src.Datasource) {
			continue
		}
		c := CatalogCoverage{
			Datasource: src.Datasource, Scanned: len(src.Scanned), Unscanned: orEmpty(unscanned[src.Datasource]),
		}
		if src.Listed != nil {
			c.Listed = new(len(src.Listed))
		}
		for _, at := range src.ScannedAt {
			if c.OldestScan == nil || at.Before(*c.OldestScan) {
				c.OldestScan = &at
			}
		}
		out = append(out, c)
	}
	return out
}

func toCatalogRow(row configplan.Row, referenced int) CatalogRow {
	t := row.Table
	out := CatalogRow{
		Ref: row.Ref, Datasource: row.Datasource, Schema: t.Schema, Table: t.Source, Description: t.Description,
		View: t.Kind == entity.KindView, Columns: len(t.Attributes), ForeignKeys: len(t.ForeignKeys),
		ReferencedBy: referenced, SideEffects: t.SideEffects,
		HasKey: len(t.PrimaryKey()) > 0 || len(configplan.IdentityUniqueKeys(t)) > 0,
		Status: TableStatus(
			strings.ToUpper(string(row.Status)),
		), Added: orEmpty(row.Added), Missing: orEmpty(row.Missing),
	}
	if row.Entity != nil {
		out.Entity = optional(row.Entity.ID())
	}
	if !row.ScannedAt.IsZero() {
		out.ScannedAt = &row.ScannedAt
	}
	return out
}

// sourceOfRef is the source holding the table ref names.
func sourceOfRef(pc planContext, ref string) (configplan.Source, bool) {
	for _, src := range pc.sources {
		if strings.HasPrefix(ref, src.Datasource+".") {
			return src, true
		}
	}
	return configplan.Source{}, false
}

// CatalogTable is one scanned table in full, with the entity proposed for
// it; nil when no scanned table has the ref.
func (r *Service) CatalogTable(ctx context.Context, ref string, against Against) (*configplan.Candidate, error) {
	if _, err := auth.RequireDatasource(ctx, auth.PermWrite, refDatasource(ref)); err != nil {
		return nil, err
	}
	pc, err := r.planContextOf(ctx, against)
	if err != nil {
		return nil, err
	}
	src, ok := sourceOfRef(pc, ref)
	if !ok {
		return nil, nil
	}
	// Only the table asked for gets a candidate; names and relationships
	// still span the datasource.
	only := map[string]bool{}
	for _, t := range src.Catalog.Tables {
		if configplan.Ref(src.Datasource, t.Schema, t.Source) == ref {
			only[configplan.TableKey(t.Schema, t.Source)] = true
		}
	}
	if len(only) == 0 {
		return nil, nil
	}
	cs := configplan.Candidates(src.Datasource, src.Catalog, pc.base, nil, only)
	return &cs[0], nil
}

// CatalogLinks lists the foreign keys of a table and those referencing it,
// among the scanned tables of its datasource, with the entities reading the
// other tables.
func (r *Service) CatalogLinks(ctx context.Context, ref string, against Against) (*CatalogLinks, error) {
	if _, err := auth.RequireDatasource(ctx, auth.PermWrite, refDatasource(ref)); err != nil {
		return nil, err
	}
	pc, err := r.planContextOf(ctx, against)
	if err != nil {
		return nil, err
	}
	out := &CatalogLinks{Outgoing: []TableLink{}, Incoming: []TableLink{}}
	src, ok := sourceOfRef(pc, ref)
	if !ok {
		return out, nil
	}
	entities := configplan.ConfiguredTables(src.Datasource, src.Catalog, pc.base)
	link := func(schema, table, name string, columns, refColumns []string) TableLink {
		l := TableLink{Ref: configplan.Ref(src.Datasource, schema, table), Name: name, Columns: orEmpty(columns),
			RefColumns: orEmpty(refColumns)}
		if e, ok := entities[configplan.TableKey(schema, table)]; ok {
			l.Entity = optional(e.ID())
		}
		return l
	}
	for _, t := range src.Catalog.Tables {
		self := configplan.Ref(src.Datasource, t.Schema, t.Source)
		for _, fk := range t.ForeignKeys {
			target := configplan.Ref(src.Datasource, refSchema(t, fk), fk.RefRelation)
			if self == ref {
				out.Outgoing = append(
					out.Outgoing,
					link(refSchema(t, fk), fk.RefRelation, fk.Name, fk.Columns, fk.RefColumns),
				)
			}
			if target == ref {
				out.Incoming = append(out.Incoming, link(t.Schema, t.Source, fk.Name, fk.Columns, fk.RefColumns))
			}
		}
	}
	return out, nil
}

// PlanScope counts what a scope selects, and each of its conditions alone.
func (r *Service) PlanScope(ctx context.Context, scope configplan.Scope, against Against) (*ScopeStats, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	if err := within(who, &scope); err != nil {
		return nil, err
	}
	if err := (&configplan.Plan{Scope: scope, Import: true}).Check(); err != nil {
		return nil, err
	}
	pc, err := r.planContextOf(ctx, against)
	if err != nil {
		return nil, err
	}
	x := r.Scans.indexOf(pc)
	rows := x.Rows(scope)
	out := &ScopeStats{Total: len(rows), Samples: []CatalogRow{}, Conditions: []ScopeCondition{}}
	for _, row := range rows[:min(len(rows), 5)] {
		out.Samples = append(out.Samples, toCatalogRow(row, x.Referenced(row.Ref)))
	}
	for _, c := range scopeConditions(scope) {
		if who.Limited() {
			// Each condition alone, within what who reaches.
			c.scope.Datasources = scope.Datasources
		}
		out.Conditions = append(out.Conditions, ScopeCondition{
			Name: c.name, Value: c.value, Matched: len(x.Match(c.scope)),
		})
	}
	return out, nil
}

type scopeCondition struct {
	name, value string
	scope       configplan.Scope
}

// scopeConditions splits a scope into its conditions, each a scope alone.
func scopeConditions(s configplan.Scope) []scopeCondition {
	var out []scopeCondition
	add := func(name string, value any, alone configplan.Scope) {
		text, _ := json.Marshal(value)
		out = append(out, scopeCondition{name: name, value: string(text), scope: alone})
	}
	if len(s.Datasources) > 0 {
		add("datasources", s.Datasources, configplan.Scope{Datasources: s.Datasources})
	}
	if len(s.Schemas) > 0 {
		add("schemas", s.Schemas, configplan.Scope{Schemas: s.Schemas})
	}
	if len(s.Tables) > 0 {
		add("tables", s.Tables, configplan.Scope{Tables: s.Tables})
	}
	if s.Search != "" {
		add("search", s.Search, configplan.Scope{Search: s.Search})
	}
	if len(s.Status) > 0 {
		add("status", s.Status, configplan.Scope{Status: s.Status})
	}
	if s.Drift {
		add("drift", true, configplan.Scope{Drift: true})
	}
	if len(s.Refs) > 0 {
		add("refs", len(s.Refs), configplan.Scope{Refs: s.Refs})
	}
	return out
}
