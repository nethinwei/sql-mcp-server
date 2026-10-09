package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// Catalog pages filter every scanned table on each request. Several
// administrators page through the same filter, so the rows of a filter (but
// for the status, which only narrows them) are cached per scans, base
// configuration and published revision: a new scan, an edit of the draft or a
// publish changes the key.

const rowsCacheSize = 16

type rowsEntry struct {
	key        string
	rows       []configplan.Row
	referenced map[string]int
}

type rowsCache struct {
	mu      sync.Mutex
	entries []*rowsEntry // most recently used last
}

func (c *rowsCache) get(key string, build func() *rowsEntry) *rowsEntry {
	c.mu.Lock()
	for i, e := range c.entries {
		if e.key == key {
			c.entries = append(slices.Delete(c.entries, i, i+1), e)
			c.mu.Unlock()
			return e
		}
	}
	c.mu.Unlock()
	e := build() // outside the lock: concurrent misses build twice, never block each other
	e.key = key
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
	if len(c.entries) > rowsCacheSize {
		c.entries = c.entries[1:]
	}
	return e
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

// scopeRows are the rows of scope, but for its status and drift conditions,
// with how many foreign keys reference each table.
func (s *Scans) scopeRows(pc planContext, scope configplan.Scope) *rowsEntry {
	scope.Status, scope.Drift = nil, false
	key, _ := json.Marshal(scope)
	return s.rows.get(fmt.Sprintf("%s|%s|%d|%s", pc.fingerprint, pc.baseHash, pc.publishedID, key), func() *rowsEntry {
		return &rowsEntry{
			rows:       configplan.Rows(pc.sources, pc.base, pc.published, scope),
			referenced: referencedBy(pc.sources),
		}
	})
}

// catalogRows lists the tables matching f, with the counts by status of
// those matching it but for the status.
func (s *Scans) catalogRows(pc planContext, f CatalogFilter) ([]configplan.Row, CatalogCounts, map[string]int) {
	scope := scopeOf(f)
	entry := s.scopeRows(pc, scope)
	var counts CatalogCounts
	var out []configplan.Row
	for _, row := range entry.rows {
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
			out = append(out, row)
		}
	}
	return out, counts, entry.referenced
}

func (r *Resolver) catalog(
	ctx context.Context, f CatalogFilter, draft *DraftInput, offset, limit int,
) (*CatalogPage, error) {
	pc, err := r.planContext(ctx, draft)
	if err != nil {
		return nil, err
	}
	rows, counts, referenced := r.Scans.catalogRows(pc, f)
	offset = min(max(offset, 0), len(rows))
	page := rows[offset:min(offset+max(min(limit, 500), 0), len(rows))]
	out := &CatalogPage{Total: len(rows), Counts: &counts, Tables: make([]CatalogRow, 0, len(page)),
		Coverage: coverage(pc.sources)}
	for _, row := range page {
		out.Tables = append(out.Tables, toCatalogRow(row, referenced[row.Ref]))
	}
	return out, nil
}

// referencedBy counts the foreign keys of scanned tables referencing each
// table, by Ref.
func referencedBy(sources []configplan.Source) map[string]int {
	out := map[string]int{}
	for _, src := range sources {
		for _, t := range src.Catalog.Tables {
			for _, fk := range t.ForeignKeys {
				out[configplan.Ref(src.Datasource, refSchema(t, fk), fk.RefRelation)]++
			}
		}
	}
	return out
}

func refSchema(t entity.Entity, fk entity.ForeignKey) string {
	if fk.RefSchema == "" {
		return t.Schema
	}
	return fk.RefSchema
}

func coverage(sources []configplan.Source) []CatalogCoverage {
	out := make([]CatalogCoverage, 0, len(sources))
	unscanned := configplan.Unscanned(sources, configplan.Scope{})
	for _, src := range sources {
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

func (r *Resolver) catalogTable(ctx context.Context, ref string, draft *DraftInput) (*ImportTable, error) {
	pc, err := r.planContext(ctx, draft)
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
	t := importTable(cs[0])
	return &t, nil
}

// catalogLinks lists the foreign keys of a table and those referencing it,
// among the scanned tables of its datasource, with the entities reading the
// other tables.
func (r *Resolver) catalogLinks(ctx context.Context, ref string, draft *DraftInput) (*CatalogLinks, error) {
	pc, err := r.planContext(ctx, draft)
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

// planScope counts what a scope selects, and each of its conditions alone.
func (r *Resolver) planScope(ctx context.Context, v any, draft *DraftInput) (*ScopeStats, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var scope configplan.Scope
	if err := json.Unmarshal(data, &scope); err != nil {
		return nil, fmt.Errorf("scope: %w", err)
	}
	if err := (&configplan.Plan{Scope: scope, Import: true}).Check(); err != nil {
		return nil, err
	}
	pc, err := r.planContext(ctx, draft)
	if err != nil {
		return nil, err
	}
	rows := configplan.Rows(pc.sources, pc.base, pc.published, scope)
	out := &ScopeStats{Total: len(rows), Samples: []CatalogRow{}, Conditions: []ScopeCondition{}}
	referenced := r.Scans.scopeRows(pc, configplan.Scope{}).referenced
	for _, row := range rows[:min(len(rows), 5)] {
		out.Samples = append(out.Samples, toCatalogRow(row, referenced[row.Ref]))
	}
	for _, c := range scopeConditions(scope) {
		out.Conditions = append(out.Conditions, ScopeCondition{
			Name: c.name, Value: c.value, Matched: len(configplan.Rows(pc.sources, pc.base, pc.published, c.scope)),
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
