package configapp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
	"github.com/nethinwei/sql-mcp-server/x/leasing"
)

// RequestScanTimeout bounds the reads that answer within their request:
// tableComments and schemaList.
const RequestScanTimeout = 30 * time.Second

// Scans runs schema scans on a bounded pool of workers and keeps what each
// scan found, per schema, in a catalog store until the next scan of that
// schema: coming back to the console, restarting or reaching another replica
// does not scan again. Decoded schemas are cached per process and read again
// when the store holds a newer scan.
type Scans struct {
	bus   *bus.Bus
	pool  *scanjobs.Pool[struct{}]
	store catalog.Store
	mu    sync.Mutex
	cache map[scanKey]keptSchema
	clock func() time.Time
	last  time.Time
	// batches runs the scan batches this process owns.
	batches *leasing.Runner
	// built caches each source's catalog by the versions of its schemas;
	// indexes the indexed catalogs and matches the rows of catalog filters.
	builtMu sync.Mutex
	built   map[string]builtCatalog
	indexes *lru[*configplan.Index]
	matches *lru[[]int32]
}

type builtCatalog struct {
	key  string
	cat  introspect.Catalog
	kept map[string]keptSchema
}

// source is what a datasource reads; see catalog.Source.
type source = catalog.Source

func sourceOf(cfg *config.Config, datasource string) source {
	d := cfg.Databases[datasource]
	return source{Datasource: datasource, Driver: d.Driver, DSN: d.ConnectionsOrDSN()[d.Route().Read].DSN}
}

// scanKey names a kept schema; "" is the default schema of a datasource that
// cannot list its schemas.
type scanKey struct {
	source string
	schema string
}

// keptSchema is a schema as scanned, with its tables as introspected.
type keptSchema struct {
	at       time.Time
	tables   []entity.Entity
	def      string
	foldCase bool
}

type listedSchemas struct {
	all     []string
	current string
	at      time.Time
}

// NewScans returns scans that run on opts.Bus (see scanjobs.Options),
// keeping them in store (in memory when nil); Close stops them.
func NewScans(opts scanjobs.Options, store catalog.Store) *Scans {
	if store == nil {
		store = catalog.NewMemoryStore()
	}
	s := &Scans{
		bus: opts.Bus, pool: scanjobs.New[struct{}](opts), store: store, cache: map[scanKey]keptSchema{},
		clock: time.Now, built: map[string]builtCatalog{},
		indexes: newLRU[*configplan.Index](indexBudget), matches: newLRU[[]int32](matchBudget),
	}
	s.batches = leasing.New(store, leasing.Options{Now: s.now, Bus: opts.Bus})
	return s
}

// now is the store's microsecond time, increasing from call to call, so a
// scan and a listing that started after it never share a timestamp.
func (s *Scans) now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.clock().UTC().Truncate(time.Microsecond)
	if !t.After(s.last) {
		t = s.last.Add(time.Microsecond)
	}
	s.last = t
	return t
}

// Close cancels the scans and batches and stops the workers.
func (s *Scans) Close() {
	s.batches.Close()
	s.pool.Close()
}

// keep records a scan of schemas per schema.
func (s *Scans) keep(ctx context.Context, src source, schemas []string, cat introspect.Catalog) error {
	return s.store.PutCatalogSchemas(ctx, src.ID(), src.Datasource, catalog.FromCatalog(cat, schemas, s.now()))
}

// current drops what was kept for src's datasource when it read another
// database, as configured before seen, when the caller read the configuration
// naming src: what was kept since may belong to a configuration newer than the
// caller's, so a slow request or scan never drops it.
func (s *Scans) current(ctx context.Context, src source, seen time.Time) error {
	return s.store.PruneCatalog(ctx, src.Datasource, src.ID(), seen)
}

// list returns the kept schema list of src, listing it with fn when there is
// none or refresh is set. A new list drops the scans of schemas no longer
// listed, except those scanned while it was listing.
func (s *Scans) list(
	ctx context.Context, src source, refresh bool, fn func() (listedSchemas, error),
) (listedSchemas, error) {
	kept, ok, err := s.store.CatalogList(ctx, src.ID())
	if err != nil {
		return listedSchemas{}, err
	}
	if ok && !refresh {
		return listedSchemas{all: kept.Schemas, current: kept.Current, at: kept.ListedAt}, nil
	}
	started := s.now()
	listed, err := fn()
	if err != nil {
		return listed, err
	}
	listed.at = s.now()
	if err := s.store.PutCatalogList(ctx, src.ID(), src.Datasource,
		catalog.List{Schemas: listed.all, Current: listed.current, ListedAt: listed.at}); err != nil {
		return listed, err
	}
	versions, err := s.store.CatalogVersions(ctx, src.ID())
	if err != nil {
		return listed, err
	}
	var gone []string
	for _, v := range versions {
		if v.Name != "" && !slices.Contains(listed.all, v.Name) {
			gone = append(gone, v.Name)
		}
	}
	return listed, s.store.DeleteCatalogSchemas(ctx, src.ID(), gone, started)
}

// kept returns every kept schema of src by name, reading from the store only
// the schemas scanned since the cache read them.
func (s *Scans) kept(ctx context.Context, src source) (map[string]keptSchema, error) {
	versions, err := s.store.CatalogVersions(ctx, src.ID())
	if err != nil {
		return nil, err
	}
	out := make(map[string]keptSchema, len(versions))
	var stale []string
	s.mu.Lock()
	for _, v := range versions {
		if c, ok := s.cache[scanKey{src.ID(), v.Name}]; ok && c.at.Equal(v.ScannedAt) {
			out[v.Name] = c
		} else {
			stale = append(stale, v.Name)
		}
	}
	for k := range s.cache {
		if _, ok := out[k.schema]; k.source == src.ID() && !ok && !slices.Contains(stale, k.schema) {
			delete(s.cache, k)
		}
	}
	s.mu.Unlock()
	if len(stale) == 0 {
		return out, nil
	}
	read, err := s.store.CatalogSchemas(ctx, src.ID(), stale)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range read {
		k := keptSchema{at: sc.ScannedAt, def: sc.Default, foldCase: sc.FoldCase,
			tables: make([]entity.Entity, 0, len(sc.Tables))}
		for _, t := range sc.Tables {
			k.tables = append(k.tables, t.Entity())
		}
		s.cache[scanKey{src.ID(), sc.Name}] = k
		out[sc.Name] = k
	}
	return out, nil
}

func (s *Scans) scanned(ctx context.Context, src source, schema string) (keptSchema, bool, error) {
	all, err := s.kept(ctx, src)
	found, ok := all[schema]
	return found, ok, err
}

// catalogOf puts every kept schema of src together, sorted by schema and
// table.
func (s *Scans) catalogOf(ctx context.Context, src source) (introspect.Catalog, map[string]keptSchema, error) {
	all, err := s.kept(ctx, src)
	if err != nil {
		return introspect.Catalog{}, nil, err
	}
	names := slices.Sorted(maps.Keys(all))
	var key strings.Builder
	for _, name := range names {
		fmt.Fprintf(&key, "%s=%d;", name, all[name].at.UnixMicro())
	}
	s.builtMu.Lock()
	b, ok := s.built[src.ID()]
	s.builtMu.Unlock()
	if ok && b.key == key.String() {
		return b.cat, b.kept, nil
	}
	var cat introspect.Catalog
	for _, kept := range all {
		cat.Default, cat.FoldCase = kept.def, kept.foldCase
		cat.Tables = append(cat.Tables, kept.tables...)
	}
	slices.SortFunc(cat.Tables, func(a, b entity.Entity) int {
		return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Source, b.Source))
	})
	cat.Index()
	s.builtMu.Lock()
	s.built[src.ID()] = builtCatalog{key: key.String(), cat: cat, kept: all}
	s.builtMu.Unlock()
	return cat, all, nil
}

// Imports are the scanned tables of a datasource with the entity proposed
// for each.
type Imports struct {
	Datasource    string
	DefaultSchema string
	// Source is the token of the database the tables were read from.
	Source string
	Tables []configplan.Candidate
}

// importOf compares the tables of the kept ones of schemas (nil: all of
// them) with cfg. Names and relationships are worked out over every kept
// schema of src: a table may reference, or share its name with, a table of
// another schema.
func (s *Scans) importOf(ctx context.Context, src source, schemas []string, cfg *config.Config) (*Imports, error) {
	all, kept, err := s.catalogOf(ctx, src)
	if err != nil {
		return nil, err
	}
	shown := map[string]bool{}
	for name, k := range kept {
		if schemas == nil || slices.Contains(schemas, name) {
			for _, t := range k.tables {
				shown[configplan.TableKey(t.Schema, t.Source)] = true
			}
		}
	}
	out := &Imports{Datasource: src.Datasource, DefaultSchema: all.Default, Source: src.Token()}
	for _, c := range configplan.Candidates(src.Datasource, all, cfg, nil, nil) {
		if shown[configplan.TableKey(c.Table.Schema, c.Table.Source)] {
			out.Tables = append(out.Tables, c)
		}
	}
	return out, nil
}

func (r *Service) scans() (*Scans, error) {
	if r.Scans == nil {
		return nil, errors.New("schema scans are not available")
	}
	return r.Scans, nil
}

// scanContext checks the caller may scan datasource and returns the
// published configuration, the scans and the source the configuration names,
// dropping what was kept for the datasource's earlier sources.
func (r *Service) scanContext(ctx context.Context, datasource string) (*config.Config, *Scans, source, error) {
	if _, err := auth.RequireDatasource(ctx, auth.PermWrite, datasource); err != nil {
		return nil, nil, source{}, err
	}
	scans, err := r.scans()
	if err != nil {
		return nil, nil, source{}, err
	}
	seen := scans.now()
	cfg, err := r.SelectConfig(ctx, Against{})
	if err != nil {
		return nil, nil, source{}, err
	}
	if !hasDatasource(cfg, datasource) {
		return nil, nil, source{}, fmt.Errorf("unknown datasource %q", datasource)
	}
	src := sourceOf(cfg, datasource)
	if err := scans.current(ctx, src, seen); err != nil {
		return nil, nil, source{}, err
	}
	return cfg, scans, src, nil
}

func toScanJob(j scanjobs.Job[struct{}]) *ScanJob {
	out := &ScanJob{ID: j.ID, Datasource: j.Label, State: string(j.State)}
	if j.Err != "" {
		out.Error = &j.Err
	}
	return out
}
