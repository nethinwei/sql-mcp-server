package graph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

func scansConfig(dsn string) *config.Config {
	return &config.Config{
		Databases: map[string]config.DatabaseConfig{"shop": {Driver: "postgres", DSN: dsn}},
		Entities:  []config.EntityConfig{{Name: "accounts", DataSource: "shop", Schema: "crm"}},
	}
}

func candidatesOf(out *SchemaImport) map[string]Entity {
	byTable := map[string]Entity{}
	for _, tb := range out.Tables {
		byTable[tableKey(tb.Schema, tb.Table)] = *tb.Candidate
	}
	return byTable
}

func targets(e Entity) []string {
	var out []string
	for _, r := range e.Relationships {
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

func mustImport(t *testing.T, scans *Scans, src source, schemas []string, cfg *config.Config) *SchemaImport {
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

// A batch whose process stopped is taken over by another once its heartbeat
// is stale: what was not scanned is scanned there, and the batch ends.
func TestScanBatchIsTakenOver(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	first := NewScans(scanjobs.Options{}, store)
	blocked := func(string) func(context.Context) (struct{}, error) {
		return func(ctx context.Context) (struct{}, error) {
			<-ctx.Done()
			return struct{}{}, ctx.Err()
		}
	}
	b, err := first.startBatch(context.Background(), "shop", "root", []string{"a", "b", "c"}, blocked)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	first.Close() // the process stops while scanning
	second := NewScans(scanjobs.Options{}, store)
	t.Cleanup(second.Close)
	ownedBySecond := func(owner string) bool { return strings.HasPrefix(owner, second.batches.owner+".") }
	if second.takeOver(context.Background(), runners(nil)); ownedBySecond(batchState(t, store, b.ID).Owner) {
		t.Fatal("a batch with a fresh heartbeat was taken over")
	}
	second.clock = func() time.Time { return time.Now().Add(time.Minute) }
	second.takeOver(context.Background(), runners(nil))
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		got := batchState(t, store, b.ID)
		if !got.FinishedAt.IsZero() {
			for _, item := range got.Items {
				if item.State != "done" {
					t.Fatalf("items = %+v", got.Items)
				}
			}
			if !ownedBySecond(got.Owner) {
				t.Fatalf("owner = %s", got.Owner)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not finished: %+v", got)
		}
	}
}

// A run that lost its batch (paused past its heartbeat while another process
// took it over) writes nothing when it resumes: not its progress, not its end.
func TestScanBatchLateWriteOfALostRun(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	scans := NewScans(scanjobs.Options{}, store)
	t.Cleanup(scans.Close)
	now := time.Now()
	b := catalog.Batch{ID: "b1", Datasource: "shop", Owner: scans.batches.run(), StartedAt: now, Heartbeat: now,
		Items: []catalog.BatchItem{{Schema: "a", State: "running"}}}
	if err := store.PutBatch(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if ok, _ := store.ClaimBatch(context.Background(), "b1", "other.run", later, later); !ok {
		t.Fatal("not taken over")
	}
	late := &batchRun{batch: b}
	late.batch.Items = []catalog.BatchItem{{Schema: "a", State: "done"}}
	late.batch.FinishedAt = now
	scans.save(late)
	got := batchState(t, store, "b1")
	if got.Owner != "other.run" || got.Items[0].State != "running" || !got.FinishedAt.IsZero() || !late.lost {
		t.Fatalf("after the lost run's write: %+v (lost %v)", got, late.lost)
	}
}

func runners(err error) func(context.Context, string) (scanRunner, error) {
	return func(context.Context, string) (scanRunner, error) {
		return func(string) func(context.Context) (struct{}, error) {
			return func(context.Context) (struct{}, error) { return struct{}{}, err }
		}, nil
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
	r := &Resolver{Scans: scans}
	blocked := func(string) func(context.Context) (struct{}, error) {
		return func(ctx context.Context) (struct{}, error) {
			<-ctx.Done()
			return struct{}{}, ctx.Err()
		}
	}
	b, err := scans.startBatch(context.Background(), "shop", "root", []string{"a", "b", "c"}, blocked)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Permissions: []string{auth.PermAll}})
	if _, err := r.cancelScanBatch(ctx, b.ID); err != nil {
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
