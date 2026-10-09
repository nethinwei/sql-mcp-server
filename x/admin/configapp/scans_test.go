package configapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
		Entities:  []config.EntityConfig{{Name: "accounts", DataSource: "shop", Schema: "crm"}},
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
	b, err := first.startBatch(context.Background(), "shop", "root", "", []string{"a", "b", "c"}, blocked)
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
// took it over) writes nothing when it resumes, and stops.
func TestScanBatchLateWriteOfALostRun(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	scans := NewScans(scanjobs.Options{}, store)
	t.Cleanup(scans.Close)
	now := time.Now()
	b := catalog.Batch{ID: "b1", Datasource: "shop", Owner: scans.batches.run(), StartedAt: now, Heartbeat: now,
		Items: []catalog.BatchItem{{Schema: "a", State: catalog.ItemRunning}}}
	if _, _, err := store.CreateBatch(context.Background(), b, 1); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if ok, _ := store.ClaimBatch(context.Background(), "b1", "other.run", later, later); !ok {
		t.Fatal("not taken over")
	}
	stopped := false
	late := &batchRun{id: "b1", owner: b.Owner, stop: func() { stopped = true }}
	if scans.move(late, catalog.BatchItem{Schema: "a", State: catalog.ItemDone}, catalog.ItemRunning) {
		t.Fatal("the lost run moved an item")
	}
	got := batchState(t, store, "b1")
	if got.Owner != "other.run" || got.Items[0].State != catalog.ItemRunning || !late.lost.Load() || !stopped {
		t.Fatalf("after the lost run's write: %+v (lost %v, stopped %v)", got, late.lost.Load(), stopped)
	}
}

// A scan that fails is tried again after a backoff, up to batchAttempts
// scans; one that keeps failing ends failed with why.
func TestScanBatchRetries(t *testing.T) {
	t.Parallel()
	store := catalog.NewMemoryStore()
	scans := NewScans(scanjobs.Options{}, store)
	t.Cleanup(scans.Close)
	scans.batches.backoff = 10 * time.Millisecond
	var calls sync.Map
	flaky := func(schema string) func(context.Context) (struct{}, error) {
		return func(context.Context) (struct{}, error) {
			n, _ := calls.LoadOrStore(schema, new(atomic.Int32))
			if c := n.(*atomic.Int32).Add(1); schema == "broken" || c == 1 {
				return struct{}{}, fmt.Errorf("scan %d of %s failed", c, schema)
			}
			return struct{}{}, nil
		}
	}
	b, err := scans.startBatch(context.Background(), "shop", "root", "", []string{"broken", "flaky"}, flaky)
	if err != nil {
		t.Fatal(err)
	}
	got := waitFinished(t, store, b.ID)
	if broken := got.Items[0]; broken.State != catalog.ItemFailed || broken.Attempts != batchAttempts ||
		broken.Error != "scan 3 of broken failed" {
		t.Fatalf("broken = %+v", broken)
	}
	if flaky := got.Items[1]; flaky.State != catalog.ItemDone || flaky.Attempts != 2 || flaky.Error != "" {
		t.Fatalf("flaky = %+v", flaky)
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
}

// failingMoves fails the item moves of a store while fail says so.
type failingMoves struct {
	catalog.Store
	fail func() bool
}

func (f failingMoves) MoveItem(
	ctx context.Context, id, owner string, now time.Time, it catalog.BatchItem, from ...string,
) (bool, error) {
	if f.fail() {
		return false, errors.New("store unavailable")
	}
	return f.Store.MoveItem(ctx, id, owner, now, it, from...)
}

// A store that fails a moment does not lose work: the write is tried again
// and the batch goes on. One that keeps failing stops the run, leaving the
// batch unfinished, as last recorded, for a takeover; nothing is canceled.
func TestScanBatchStoreFailures(t *testing.T) {
	t.Parallel()
	succeeds := func(string) func(context.Context) (struct{}, error) {
		return func(context.Context) (struct{}, error) { return struct{}{}, nil }
	}
	var failures atomic.Int32
	failures.Store(1)
	once := failingMoves{Store: catalog.NewMemoryStore(), fail: func() bool { return failures.Add(-1) >= 0 }}
	scans := NewScans(scanjobs.Options{}, once)
	t.Cleanup(scans.Close)
	b, err := scans.startBatch(context.Background(), "shop", "root", "", []string{"a"}, succeeds)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitFinished(t, once, b.ID); got.Items[0].State != catalog.ItemDone {
		t.Fatalf("after a failure for a moment: %+v", got.Items)
	}
	broken := failingMoves{Store: catalog.NewMemoryStore(), fail: func() bool { return true }}
	stuck := NewScans(scanjobs.Options{}, broken)
	t.Cleanup(stuck.Close)
	b, err = stuck.startBatch(context.Background(), "shop", "root", "", []string{"a"}, succeeds)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		stuck.batches.mu.Lock()
		_, running := stuck.batches.running[b.ID]
		stuck.batches.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run does not stop")
		}
	}
	if got := batchState(t, broken, b.ID); !got.FinishedAt.IsZero() || got.Items[0].State != catalog.ItemPending {
		t.Fatalf("a run the store failed ended the batch: %+v", got)
	}
}

// lostAnswers makes the item moves of a store and then, while lose says so,
// answers with an error as if the connection broke after the commit.
type lostAnswers struct {
	catalog.Store
	lose func() bool
}

func (l lostAnswers) MoveItem(
	ctx context.Context, id, owner string, now time.Time, it catalog.BatchItem, from ...string,
) (bool, error) {
	moved, err := l.Store.MoveItem(ctx, id, owner, now, it, from...)
	if err == nil && l.lose() {
		return false, errors.New("connection reset")
	}
	return moved, err
}

// A move made whose answer was lost counts as made: the schema is scanned,
// not skipped.
func TestScanBatchLostAnswers(t *testing.T) {
	t.Parallel()
	var lose atomic.Int32
	lose.Store(1)
	store := lostAnswers{Store: catalog.NewMemoryStore(), lose: func() bool { return lose.Add(-1) >= 0 }}
	scans := NewScans(scanjobs.Options{}, store)
	t.Cleanup(scans.Close)
	var runs atomic.Int32
	counted := func(string) func(context.Context) (struct{}, error) {
		return func(context.Context) (struct{}, error) {
			runs.Add(1)
			return struct{}{}, nil
		}
	}
	b, err := scans.startBatch(context.Background(), "shop", "root", "", []string{"a"}, counted)
	if err != nil {
		t.Fatal(err)
	}
	got := waitFinished(t, store, b.ID)
	if got.Items[0].State != catalog.ItemDone || got.Items[0].Attempts != 1 || runs.Load() != 1 {
		t.Fatalf("after a lost answer: %+v, scanned %d times", got.Items, runs.Load())
	}
}

// A takeover that cannot queue the schemas being scanned again does not run
// the batch: it stays unfinished, as it was, and a later takeover runs it.
func TestScanBatchTakeoverWaitsForTheStore(t *testing.T) {
	t.Parallel()
	var down atomic.Bool
	down.Store(true)
	store := failingMoves{Store: catalog.NewMemoryStore(), fail: down.Load}
	old := time.Now().Add(-time.Hour)
	if _, _, err := store.CreateBatch(context.Background(), catalog.Batch{ID: "b1", Datasource: "shop",
		Owner: "gone.run", StartedAt: old, Heartbeat: old, Items: []catalog.BatchItem{
			{Schema: "a", State: catalog.ItemRunning, Attempts: 1}, {Schema: "b", State: catalog.ItemPending}}}, 1); err != nil {
		t.Fatal(err)
	}
	scans := NewScans(scanjobs.Options{}, store)
	t.Cleanup(scans.Close)
	scans.takeOver(context.Background(), runners(nil))
	if got := batchState(t, store, "b1"); !got.FinishedAt.IsZero() || got.Items[0].State != catalog.ItemRunning {
		t.Fatalf("a takeover the store failed ran the batch: %+v", got)
	}
	down.Store(false)
	scans.clock = func() time.Time { return time.Now().Add(time.Minute) }
	scans.takeOver(context.Background(), runners(nil))
	got := waitFinished(t, store, "b1")
	for _, it := range got.Items {
		if it.State != catalog.ItemDone {
			t.Fatalf("after the store is back: %+v", got.Items)
		}
	}
}
