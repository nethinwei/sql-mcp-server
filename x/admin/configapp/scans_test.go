package configapp

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

func scansConfig(dsn string) *config.Config {
	return &config.Config{
		Databases: map[string]config.DatabaseConfig{"shop": {Driver: "postgres", DSN: dsn}},
		Entities: []config.EntityConfig{{Name: "accounts", DataSource: "shop", Schema: "crm",
			Fields: []config.FieldConfig{{Name: "id"}}}},
	}
}

func table(schema, name string, fks ...entity.ForeignKey) entity.Entity {
	return entity.Entity{
		Name: name, Source: name, Schema: schema, ForeignKeys: fks,
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "user_id"}},
		Keys:       []entity.Key{{Columns: []string{"id"}, Primary: true}},
	}
}

// candidate is the entity proposed for a scanned table, with its ID.
type candidate struct {
	ID     string
	Entity config.EntityConfig
}

func candidatesOf(out *Imports) map[string]candidate {
	byTable := map[string]candidate{}
	for _, c := range out.Tables {
		byTable[configplan.TableKey(c.Table.Schema, c.Table.Source)] = candidate{ID: c.ID, Entity: c.Entity}
	}
	return byTable
}

func targets(c candidate) []string {
	var out []string
	for _, r := range c.Entity.Relationships {
		out = append(out, r.Cardinality+" "+r.Target)
	}
	return out
}

// Schemas scanned one at a time are compared together: same-named tables
// are told apart by their IDs and foreign keys across schemas become relationships,
// to kept tables and to configured entities of schemas not scanned.
func TestKeptSchemasAreImportedTogether(t *testing.T) {
	t.Parallel()
	scans := NewScans(scanjobs.Options{}, nil)
	t.Cleanup(scans.Close)
	cfg := scansConfig("postgres://a/shop")
	src := sourceOf(cfg, "shop")
	mustKeep(t, scans, src, []string{"public"}, introspect.Catalog{
		Tables: []entity.Entity{table("public", "users"), table("public", "customers")},
	})
	mustKeep(t, scans, src, []string{"sales"}, introspect.Catalog{Tables: []entity.Entity{
		table("sales", "users"),
		table("sales", "orders",
			entity.ForeignKey{Columns: []string{"user_id"}, RefSchema: "public", RefRelation: "customers",
				RefColumns: []string{"id"}},
			entity.ForeignKey{Columns: []string{"id"}, RefSchema: "crm", RefRelation: "accounts",
				RefColumns: []string{"id"}}),
	}})
	sales := candidatesOf(mustImport(t, scans, src, []string{"sales"}, cfg))
	if len(sales) != 2 || sales["sales.users"].ID != "shop.sales.users" {
		t.Fatalf("sales = %+v", sales)
	}
	if got := targets(sales["sales.orders"]); len(got) != 2 ||
		got[0] != "belongs-to shop.public.customers" || got[1] != "belongs-to shop.crm.accounts" {
		t.Fatalf("orders relationships = %v", got)
	}
	if both := mustImport(t, scans, src, nil, cfg); len(both.Tables) != 4 {
		t.Fatalf("both schemas in one call = %+v", both.Tables)
	}
	public := candidatesOf(mustImport(t, scans, src, []string{"public"}, cfg))
	if public["public.users"].ID != "shop.public.users" ||
		targets(public["public.customers"])[0] != "has-many shop.sales.orders" {
		t.Fatalf("public = %+v", public)
	}
}

func mustKeep(t *testing.T, scans *Scans, src source, schemas []string, cat introspect.Catalog) {
	t.Helper()
	if err := scans.keep(context.Background(), src, schemas, cat); err != nil {
		t.Fatal(err)
	}
}

func mustImport(t *testing.T, scans *Scans, src source, schemas []string, cfg *config.Config) *Imports {
	t.Helper()
	out, err := scans.importOf(context.Background(), src, schemas, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func isKept(t *testing.T, scans *Scans, src source, schema string) bool {
	t.Helper()
	_, ok, err := scans.scanned(context.Background(), src, schema)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func keepSchemas(t *testing.T, scans *Scans, src source, tables ...entity.Entity) {
	t.Helper()
	var schemas []string
	for _, tb := range tables {
		schemas = append(schemas, tb.Schema)
	}
	mustKeep(t, scans, src, schemas, introspect.Catalog{Tables: tables})
}

// What was kept for a datasource is dropped once it reads another database,
// and a refreshed list drops the schemas that are gone.
func TestKeptScansFollowTheDatabase(t *testing.T) {
	t.Parallel()
	scans := NewScans(scanjobs.Options{}, nil)
	t.Cleanup(scans.Close)
	old := sourceOf(scansConfig("postgres://a/shop"), "shop")
	keepSchemas(t, scans, old, table("public", "users"), table("sales", "orders"))
	moved := scansConfig("postgres://b/shop")
	if out := mustImport(t, scans, sourceOf(moved, "shop"), []string{"public"}, moved); len(out.Tables) != 0 {
		t.Fatalf("another database shows the old scan: %+v", out.Tables)
	}
	_ = scans.current(context.Background(), sourceOf(moved, "shop"), time.Now())
	if isKept(t, scans, old, "public") {
		t.Fatal("the old database's scan must be dropped")
	}
	keepSchemas(t, scans, old, table("public", "users"), table("sales", "orders"))
	if _, err := scans.list(context.Background(), old, true, func() (listedSchemas, error) {
		return listedSchemas{all: []string{"public"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if isKept(t, scans, old, "sales") {
		t.Fatal("a schema no longer listed must be dropped")
	}
	if !isKept(t, scans, old, "public") {
		t.Fatal("a listed schema keeps its scan")
	}
}

// Requests and scans for the old database that finish after the datasource
// moved, and after the new database was scanned, keep the new one's scans.
func TestLateRequestsKeepNewerScans(t *testing.T) {
	t.Parallel()
	scans := NewScans(scanjobs.Options{}, nil)
	t.Cleanup(scans.Close)
	old := sourceOf(scansConfig("postgres://a/shop"), "shop")
	moved := scansConfig("postgres://b/shop")
	seenOld := time.Now()
	keepSchemas(t, scans, sourceOf(moved, "shop"), table("public", "accounts"))
	keepSchemas(t, scans, old, table("public", "users"))
	if out := mustImport(t, scans, old, []string{"public"}, moved); len(out.Tables) != 1 ||
		out.Source == sourceOf(moved, "shop").Token() {
		t.Fatalf("the late scan answers for its own database: %+v", out)
	}
	_ = scans.current(context.Background(), old, seenOld)
	if _, err := scans.list(context.Background(), old, true, func() (listedSchemas, error) {
		return listedSchemas{all: []string{"public"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !isKept(t, scans, sourceOf(moved, "shop"), "public") {
		t.Fatal("a late request for the old database must not drop the new one's scans")
	}
}

// Scans are kept in the catalog store: another replica, or the process after
// a restart, reads them, and a newer scan replaces what it cached.
func TestScansAreSharedThroughTheStore(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	a, b := NewScans(scanjobs.Options{}, store), NewScans(scanjobs.Options{}, store)
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	cfg := scansConfig("postgres://a/shop")
	src := sourceOf(cfg, "shop")
	keepSchemas(t, a, src, table("public", "users"))
	if got := mustImport(t, b, src, nil, cfg); len(got.Tables) != 1 {
		t.Fatalf("the other replica sees %+v", got.Tables)
	}
	keepSchemas(t, a, src, table("public", "users"), table("public", "orders"))
	if got := mustImport(t, b, src, nil, cfg); len(got.Tables) != 2 {
		t.Fatalf("a newer scan is not read again: %+v", got.Tables)
	}
}

func waitFinished(t *testing.T, store catalog.Store, id string) catalog.Batch {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if got := batchState(t, store, id); !got.FinishedAt.IsZero() {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("batch %s not finished: %+v", id, batchState(t, store, id))
		}
	}
}

func batchState(t *testing.T, store catalog.Store, id string) catalog.Batch {
	t.Helper()
	b, ok, err := store.Batch(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("batch %s: %v %v", id, ok, err)
	}
	return b
}

// Canceling a batch stops its scans and settles what was left.
func TestScanBatchCancel(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	scans := NewScans(scanjobs.Options{}, store)
	t.Cleanup(scans.Close)
	r := &Service{Scans: scans}
	blocked := func(string) func(context.Context) (struct{}, error) {
		return func(ctx context.Context) (struct{}, error) {
			<-ctx.Done()
			return struct{}{}, ctx.Err()
		}
	}
	b, err := scans.startBatch(context.Background(), "shop", "root", "", []string{"a", "b", "c"}, blocked)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Permissions: []string{auth.PermAll}})
	if _, err := r.CancelScanBatch(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		got := batchState(t, store, b.ID)
		if !got.FinishedAt.IsZero() {
			if view := viewBatch(got); view.State != "canceled" || view.Canceled != 3 || !got.Canceled {
				t.Fatalf("view = %+v", view)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not finished: %+v", got)
		}
	}
}

// Batches cannot pile up: a datasource runs a few at a time, of a bounded
// number of schemas each.
func TestScanBatchQuotas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	revs := revision.NewMemoryStore(nil)
	d, _ := revs.Create(ctx, revision.Draft{Payload: []byte("databases:\n  shop:\n    driver: postgres\n    dsn: x\n")})
	if _, err := revs.Publish(ctx, d.ID, 0, revision.Meta{}); err != nil {
		t.Fatal(err)
	}
	store := catalog.NewMemoryStore()
	scans := NewScans(scanjobs.Options{}, store)
	t.Cleanup(scans.Close)
	app := &Service{Store: revs, Scans: scans}
	for i := range batchOpen {
		if _, _, err := store.CreateBatch(ctx, catalog.Batch{ID: fmt.Sprint("open", i), Datasource: "shop",
			Owner: "elsewhere", StartedAt: time.Now(), Heartbeat: time.Now()}, batchOpen); err != nil {
			t.Fatal(err)
		}
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{Username: "root", Permissions: []string{auth.PermAll}})
	if _, err := app.StartScanBatch(admin, ScanBatchInput{Datasource: "shop", Schemas: []string{"public"}}); err == nil ||
		!strings.Contains(err.Error(), "running") {
		t.Fatalf("a batch past the quota: %v", err)
	}
	many := make([]string, batchSchemas+1)
	for i := range many {
		many[i] = fmt.Sprint("s", i)
	}
	_, err := app.StartScanBatch(admin, ScanBatchInput{Datasource: "shop", Schemas: many})
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("a batch of too many schemas: %v", err)
	}
}

// A request sent again with its key gets the batch it started, which runs
// once.
func TestScanBatchRequestKey(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	scans := NewScans(scanjobs.Options{}, store)
	t.Cleanup(scans.Close)
	var runs atomic.Int32
	counted := func(string) func(context.Context) (struct{}, error) {
		return func(context.Context) (struct{}, error) {
			runs.Add(1)
			return struct{}{}, nil
		}
	}
	first, err := scans.startBatch(context.Background(), "shop", "root", "k1", []string{"a"}, counted)
	if err != nil {
		t.Fatal(err)
	}
	again, err := scans.startBatch(context.Background(), "shop", "root", "k1", []string{"a"}, counted)
	if err != nil || again.ID != first.ID {
		t.Fatalf("the request again = %+v, %v", again, err)
	}
	waitFinished(t, store, first.ID)
	if runs.Load() != 1 {
		t.Fatalf("scanned %d times", runs.Load())
	}
	// The key of another request: its batch would leave b unscanned.
	if _, err := scans.startBatch(context.Background(), "shop", "root", "k1", []string{"b"}, counted); err == nil {
		t.Fatal("a key reused for other schemas got the batch of the first")
	}
	other, err := scans.startBatch(context.Background(), "shop", "bob", "k1", []string{"b"}, counted)
	if err != nil || other.ID == first.ID {
		t.Fatalf("another administrator's request = %+v, %v", other, err)
	}
}

// A catalog of schemas scanned at different times takes the default schema
// and case of the last scan, whatever order a map visits them in.
func TestCatalogDefaultIsTheLastScanned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := catalog.NewMemoryStore()
	src := source{Datasource: "shop", Driver: "postgres", DSN: "x"}
	t0 := time.Now()
	var schemas []catalog.Schema
	for i := range 8 {
		schemas = append(schemas, catalog.Schema{Name: fmt.Sprint("s", i), ScannedAt: t0.Add(time.Duration(i) * time.Second),
			Default: fmt.Sprint("d", i), Tables: []catalog.Table{}})
	}
	if err := store.PutCatalogSchemas(ctx, src.ID(), "shop", schemas); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		scans := NewScans(scanjobs.Options{}, store)
		cat, _, err := scans.catalogOf(ctx, src)
		scans.Close()
		if err != nil || cat.Default != "d7" {
			t.Fatalf("default = %q, %v", cat.Default, err)
		}
	}
}
