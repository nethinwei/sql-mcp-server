package graph

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
)

// requestScanTimeout bounds the reads that answer within their request:
// tableComments and schemaList.
const requestScanTimeout = 30 * time.Second

// Scans runs schema scans on a bounded pool of workers and keeps what each
// scan found, per schema, until the next scan of that schema: coming back to
// the console does not scan again.
type Scans struct {
	pool    *scanjobs.Pool[struct{}]
	mu      sync.Mutex
	schemas map[scanKey]scannedSchema
	lists   map[source]listedSchemas
	now     func() time.Time
}

// source is what a datasource reads: its driver and read connection. What is
// kept belongs to a source, so a datasource that keeps its name but reads
// another database does not show what the old one had.
type source struct{ datasource, driver, dsn string }

// token names src without revealing its DSN.
func (src source) token() string {
	sum := sha256.Sum256([]byte(src.driver + "\x00" + src.dsn))
	return hex.EncodeToString(sum[:8])
}

func sourceOf(cfg *config.Config, datasource string) source {
	d := cfg.Databases[datasource]
	return source{datasource, d.Driver, d.ConnectionsOrDSN()[d.Route().Read].DSN}
}

// scanKey names a kept schema; "" is the default schema of a datasource that
// cannot list its schemas.
type scanKey struct {
	source source
	schema string
}

type scannedSchema struct {
	cat introspect.Catalog
	at  time.Time
}

type listedSchemas struct {
	all     []string
	current string
	at      time.Time
}

// NewScans starts the scan workers; Close stops them.
func NewScans(opts scanjobs.Options) *Scans {
	return &Scans{
		pool: scanjobs.New[struct{}](opts), schemas: map[scanKey]scannedSchema{},
		lists: map[source]listedSchemas{}, now: time.Now,
	}
}

// Close cancels the scans and stops the workers.
func (s *Scans) Close() { s.pool.Close() }

// keep records a scan of schemas (keys of scanKey) per schema.
func (s *Scans) keep(src source, schemas []string, cat introspect.Catalog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := s.now()
	bySchema := map[string][]entity.Entity{}
	for _, t := range cat.Tables {
		bySchema[t.Schema] = append(bySchema[t.Schema], t)
	}
	for _, schema := range schemas {
		part := introspect.Catalog{Tables: bySchema[schema], Default: cat.Default, FoldCase: cat.FoldCase}
		if schema == "" { // the default schema: all of the scan
			part.Tables = cat.Tables
		}
		s.schemas[scanKey{src, schema}] = scannedSchema{cat: part, at: at}
	}
}

// current drops what was kept for src's datasource when it read another
// database, as configured before seen, when the caller read the configuration
// naming src: what was kept since may belong to a configuration newer than the
// caller's, so a slow request or scan never drops it.
func (s *Scans) current(src source, seen time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, kept := range s.schemas {
		if k.source.datasource == src.datasource && k.source != src && kept.at.Before(seen) {
			delete(s.schemas, k)
		}
	}
	for k, listed := range s.lists {
		if k.datasource == src.datasource && k != src && listed.at.Before(seen) {
			delete(s.lists, k)
		}
	}
}

// list returns the kept schema list of src, listing it with fn when there is
// none or refresh is set. A new list drops the scans of schemas no longer
// listed, except those scanned while it was listing.
func (s *Scans) list(src source, refresh bool, fn func() (listedSchemas, error)) (listedSchemas, error) {
	s.mu.Lock()
	listed, ok := s.lists[src]
	s.mu.Unlock()
	if ok && !refresh {
		return listed, nil
	}
	started := s.now()
	listed, err := fn()
	if err != nil {
		return listed, err
	}
	listed.at = s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists[src] = listed
	for k, kept := range s.schemas {
		if k.source == src && k.schema != "" && !slices.Contains(listed.all, k.schema) && kept.at.Before(started) {
			delete(s.schemas, k)
		}
	}
	return listed, nil
}

func (s *Scans) scanned(src source, schema string) (scannedSchema, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found, ok := s.schemas[scanKey{src, schema}]
	return found, ok
}

// importOf compares the tables of the kept ones of schemas (nil: all of
// them) with cfg. Names
// and relationships are worked out over every kept schema of src: a table may
// reference, or share its name with, a table of another schema.
func (s *Scans) importOf(src source, schemas []string, cfg *config.Config) *SchemaImport {
	s.mu.Lock()
	var all introspect.Catalog
	shown := map[string]bool{}
	for k, kept := range s.schemas {
		if k.source != src {
			continue
		}
		all.Default, all.FoldCase = kept.cat.Default, kept.cat.FoldCase
		all.Tables = append(all.Tables, kept.cat.Tables...)
		if schemas == nil || slices.Contains(schemas, k.schema) {
			for _, t := range kept.cat.Tables {
				shown[tableKey(t.Schema, t.Source)] = true
			}
		}
	}
	s.mu.Unlock()
	slices.SortFunc(all.Tables, func(a, b entity.Entity) int {
		return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Source, b.Source))
	})
	all.Index()
	out := buildSchemaImport(src.datasource, all, cfg)
	out.Tables = slices.DeleteFunc(out.Tables, func(t ImportTable) bool { return !shown[tableKey(t.Schema, t.Table)] })
	out.Source = src.token()
	return out
}

func (r *Resolver) scans() (*Scans, error) {
	if r.Scans == nil {
		return nil, errors.New("schema scans are not available")
	}
	return r.Scans, nil
}

// scanContext checks the caller may scan datasource and returns the
// published configuration, the scans and the source the configuration names,
// dropping what was kept for the datasource's earlier sources.
func (r *Resolver) scanContext(ctx context.Context, datasource string) (*config.Config, *Scans, source, error) {
	if _, err := auth.Require(ctx, auth.PermWrite); err != nil {
		return nil, nil, source{}, err
	}
	scans, err := r.scans()
	if err != nil {
		return nil, nil, source{}, err
	}
	seen := scans.now()
	cfg, err := r.selectConfig(ctx, nil, nil)
	if err != nil {
		return nil, nil, source{}, err
	}
	if !hasDatasource(cfg, datasource) {
		return nil, nil, source{}, fmt.Errorf("unknown datasource %q", datasource)
	}
	src := sourceOf(cfg, datasource)
	scans.current(src, seen)
	return cfg, scans, src, nil
}

func toScanJob(j scanjobs.Job[struct{}]) *ScanJob {
	out := &ScanJob{ID: j.ID, Datasource: j.Label, State: ScanState(j.State)}
	if j.Err != "" {
		out.Error = &j.Err
	}
	return out
}
