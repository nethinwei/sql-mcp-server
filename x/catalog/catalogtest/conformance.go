// Package catalogtest provides the conformance suite every catalog.Store
// implementation must pass, so SQL stores keep the MemoryStore semantics.
package catalogtest

import (
	"context"
	"reflect"
	"testing"
	"time"

	"errors"
	"fmt"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
	"sync"
	"sync/atomic"
)

// NewStore returns an empty store.
type NewStore func(t *testing.T) catalog.Store

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)

func table(schema, name string) catalog.Table {
	return catalog.Table{
		Schema: schema, Name: name, Description: "说明 " + name,
		Columns: []catalog.Column{
			{Name: "id", Type: "bigint", AutoIncrement: true},
			{Name: "note", Type: "text", Nullable: true, Description: "备注"},
		},
		Keys:    []catalog.Key{{Name: "pk", Columns: []string{"id"}, Primary: true}},
		Indexes: []catalog.Index{{Name: "pk", Method: "btree", Parts: []string{"id"}, Unique: true, Primary: true}},
		ForeignKeys: []catalog.ForeignKey{
			{Name: "fk", Columns: []string{"id"}, RefTable: "other", RefColumns: []string{"id"}},
		},
	}
}

// Run runs the suite.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	cases := map[string]func(*testing.T, catalog.Store){
		"Lists":            testLists,
		"SchemasRoundTrip": testSchemasRoundTrip,
		"Prune":            testPrune,
		"DeleteSchemas":    testDeleteSchemas,
		"Batches":          testBatches,
		"BatchRequestKeys": testBatchRequestKeys,
		"ClaimBatches":     testClaimBatches,
		"MoveItems":        testMoveItems,
		"CreateBatchQuota": testCreateBatchQuota,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newStore(t)) })
	}
}

func testLists(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	if _, ok, err := s.CatalogList(ctx, "src"); ok || err != nil {
		t.Fatalf("empty store: %v %v", ok, err)
	}
	want := catalog.List{Schemas: []string{"a", "b"}, Current: "a", ListedAt: t0}
	for range 2 { // a second put replaces the first
		if err := s.PutCatalogList(ctx, "src", "ds", want); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.CatalogList(ctx, "src")
	if err != nil || !ok || !reflect.DeepEqual(got, want) || !got.ListedAt.Equal(t0) {
		t.Fatalf("list = %+v %v %v", got, ok, err)
	}
}

func testSchemasRoundTrip(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	a := catalog.Schema{Name: "a", ScannedAt: t0, Default: "a", FoldCase: true,
		Tables: []catalog.Table{table("a", "t1"), table("a", "t2")}}
	empty := catalog.Schema{Name: "", ScannedAt: t0.Add(time.Second), Tables: []catalog.Table{}}
	if err := s.PutCatalogSchemas(ctx, "src", "ds", []catalog.Schema{a, empty}); err != nil {
		t.Fatal(err)
	}
	versions, err := s.CatalogVersions(ctx, "src")
	if err != nil {
		t.Fatal(err)
	}
	wantVersions := []catalog.Version{
		{Name: "", ScannedAt: empty.ScannedAt, Tables: 0},
		{Name: "a", ScannedAt: t0, Tables: 2},
	}
	if !reflect.DeepEqual(versions, wantVersions) {
		t.Fatalf("versions = %+v", versions)
	}
	got, err := s.CatalogSchemas(ctx, "src", []string{"a", "missing"})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], a) {
		t.Fatalf("schemas = %+v %v", got, err)
	}
	a.Tables = a.Tables[:1]
	a.ScannedAt = t0.Add(time.Minute)
	if err := s.PutCatalogSchemas(ctx, "src", "ds", []catalog.Schema{a}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.CatalogSchemas(ctx, "src", []string{"a"})
	if len(got) != 1 || len(got[0].Tables) != 1 || !got[0].ScannedAt.Equal(a.ScannedAt) {
		t.Fatalf("replaced = %+v", got)
	}
	if other, _ := s.CatalogVersions(ctx, "other"); len(other) != 0 {
		t.Fatalf("another source sees %+v", other)
	}
}

func testPrune(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	for _, src := range []string{"old", "new"} {
		_ = s.PutCatalogList(ctx, src, "ds", catalog.List{Schemas: []string{"a"}, ListedAt: t0})
		_ = s.PutCatalogSchemas(ctx, src, "ds", []catalog.Schema{{Name: "a", ScannedAt: t0, Tables: []catalog.Table{}}})
	}
	_ = s.PutCatalogSchemas(
		ctx,
		"late",
		"ds",
		[]catalog.Schema{{Name: "a", ScannedAt: t0.Add(time.Hour), Tables: []catalog.Table{}}},
	)
	_ = s.PutCatalogSchemas(
		ctx,
		"elsewhere",
		"other",
		[]catalog.Schema{{Name: "a", ScannedAt: t0, Tables: []catalog.Table{}}},
	)
	if err := s.PruneCatalog(ctx, "ds", "new", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.CatalogList(ctx, "old"); ok {
		t.Fatal("the old source's list survived")
	}
	for src, want := range map[string]int{"old": 0, "new": 1, "late": 1, "elsewhere": 1} {
		if v, _ := s.CatalogVersions(ctx, src); len(v) != want {
			t.Errorf("%s keeps %d schemas, want %d", src, len(v), want)
		}
	}
}

func testDeleteSchemas(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	_ = s.PutCatalogSchemas(ctx, "src", "ds", []catalog.Schema{
		{Name: "a", ScannedAt: t0, Tables: []catalog.Table{}},
		{Name: "b", ScannedAt: t0.Add(time.Hour), Tables: []catalog.Table{}},
		{Name: "c", ScannedAt: t0, Tables: []catalog.Table{}},
	})
	if err := s.DeleteCatalogSchemas(ctx, "src", []string{"a", "b"}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	v, _ := s.CatalogVersions(ctx, "src")
	if len(v) != 2 || v[0].Name != "b" || v[1].Name != "c" {
		t.Fatalf("versions = %+v", v)
	}
}

// item is a schema of a batch in a state.
func item(schema, state string) catalog.BatchItem {
	return catalog.BatchItem{Schema: schema, State: state}
}

func create(t *testing.T, s catalog.Store, b catalog.Batch) {
	t.Helper()
	if b.Items == nil {
		b.Items = []catalog.BatchItem{}
	}
	if _, created, err := s.CreateBatch(context.Background(), b, 100); err != nil || !created {
		t.Fatalf("create %s: %v %v", b.ID, created, err)
	}
}

func testBatches(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	b := catalog.Batch{ID: "b1", Datasource: "ds", StartedBy: "root", Owner: "p1", StartedAt: t0, Heartbeat: t0,
		Items: []catalog.BatchItem{item("a", catalog.ItemPending), {Schema: "b", State: catalog.ItemFailed,
			Error: "boom", Attempts: 3, NotBefore: t0.Add(time.Second)}}}
	create(t, s, b)
	create(t, s, catalog.Batch{ID: "b2", Datasource: "ds", Owner: "p1", StartedAt: t0.Add(time.Minute), Heartbeat: t0})
	if err := s.FinishBatch(ctx, "b2", "p1", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	create(t, s, catalog.Batch{ID: "b3", Datasource: "other", StartedAt: t0})
	got, ok, err := s.Batch(ctx, "b1")
	if err != nil || !ok || !reflect.DeepEqual(got, b) {
		t.Fatalf("batch = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.Batch(ctx, "missing"); ok {
		t.Fatal("a missing batch is found")
	}
	list, _ := s.Batches(ctx, "ds", 10)
	if len(list) != 2 || list[0].ID != "b2" || list[0].FinishedAt.IsZero() {
		t.Fatalf("batches = %+v", list)
	}
	if one, _ := s.Batches(ctx, "ds", 1); len(one) != 1 {
		t.Fatalf("limit: %+v", one)
	}
	open, _ := s.UnfinishedBatches(ctx)
	if len(open) != 2 || open[0].ID != "b1" || open[1].ID != "b3" || len(open[0].Items) != 2 {
		t.Fatalf("unfinished = %+v", open)
	}
	if err := s.CancelBatch(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if moved, err := s.MoveItem(ctx, "b1", "p1", t0.Add(time.Second), item("a", catalog.ItemRunning),
		catalog.ItemPending); !moved || err != nil {
		t.Fatalf("move after cancel: %v %v", moved, err)
	}
	if got, _, _ := s.Batch(ctx, "b1"); !got.Canceled || got.Items[0].State != catalog.ItemRunning {
		t.Fatalf("after cancel = %+v", got)
	}
}

// A request sent again with its key gets the batch it started.
func testBatchRequestKeys(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	b := catalog.Batch{ID: "b1", Datasource: "ds", RequestKey: "k1", StartedAt: t0, Items: []catalog.BatchItem{
		item("a", catalog.ItemPending)}}
	create(t, s, b)
	again := b
	again.ID = "b2"
	got, created, err := s.CreateBatch(ctx, again, 100)
	if err != nil || created || got.ID != "b1" || len(got.Items) != 1 {
		t.Fatalf("the same request again = %+v %v %v", got, created, err)
	}
	again.Datasource = "other"
	if _, created, _ := s.CreateBatch(ctx, again, 100); !created {
		t.Fatal("a key of another datasource")
	}
}

func testClaimBatches(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	create(t, s, catalog.Batch{ID: "b1", Datasource: "ds", Owner: "p1", StartedAt: t0, Heartbeat: t0})
	if ok, _ := s.ClaimBatch(ctx, "b1", "p2", t0, t0.Add(time.Second)); ok {
		t.Fatal("a live batch was taken over")
	}
	if ok, _ := s.ClaimBatch(ctx, "b1", "p1", t0, t0.Add(time.Second)); !ok {
		t.Fatal("the owner cannot beat")
	}
	if ok, _ := s.ClaimBatch(ctx, "b1", "p2", t0.Add(time.Minute), t0.Add(time.Minute)); !ok {
		t.Fatal("a stale batch was not taken over")
	}
	if got, _, _ := s.Batch(ctx, "b1"); got.Owner != "p2" || !got.Heartbeat.Equal(t0.Add(time.Minute)) {
		t.Fatalf("claimed = %+v", got)
	}
	if ok, _ := s.ClaimBatch(ctx, "b1", "p1", t0, t0.Add(time.Hour)); ok {
		t.Fatal("the old owner beat over the new one")
	}
	if ok, _ := s.ClaimBatch(ctx, "missing", "p1", t0, t0); ok {
		t.Fatal("claimed a missing batch")
	}
}

// Items move only from the states named, only by the owner of the unfinished
// batch: a run that lost the batch, however late it writes, changes nothing,
// and the end of a batch cancels what was left in the same step.
func testMoveItems(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	create(t, s, catalog.Batch{ID: "b1", Datasource: "ds", Owner: "p1.r1", StartedAt: t0, Heartbeat: t0,
		Items: []catalog.BatchItem{item("a", catalog.ItemPending), item("b", catalog.ItemPending)}})
	at := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Second) }
	running := catalog.BatchItem{Schema: "a", State: catalog.ItemRunning, Attempts: 1}
	if moved, err := s.MoveItem(ctx, "b1", "p1.r1", at(1), running, catalog.ItemPending); !moved || err != nil {
		t.Fatalf("pending to running: %v %v", moved, err)
	}
	if moved, err := s.MoveItem(ctx, "b1", "p1.r1", at(2), running, catalog.ItemPending); moved || err != nil {
		t.Fatalf("an item moved from a state it is not in: %v %v", moved, err)
	}
	retry := catalog.BatchItem{Schema: "a", State: catalog.ItemPending, Error: "boom", Attempts: 1, NotBefore: at(9)}
	if moved, _ := s.MoveItem(ctx, "b1", "p1.r1", at(3), retry, catalog.ItemRunning); !moved {
		t.Fatal("running back to pending")
	}
	if got, _, _ := s.Batch(ctx, "b1"); !reflect.DeepEqual(got.Items[0], retry) || !got.Heartbeat.Equal(at(3)) {
		t.Fatalf("after the retry = %+v", got)
	}
	if ok, _ := s.ClaimBatch(ctx, "b1", "p2.r2", at(60), at(60)); !ok {
		t.Fatal("not taken over")
	}
	late := catalog.BatchItem{Schema: "a", State: catalog.ItemDone}
	if _, err := s.MoveItem(ctx, "b1", "p1.r1", at(61), late, catalog.ItemPending); !errors.Is(err, catalog.ErrNotOwner) {
		t.Fatalf("the old run moved an item: %v", err)
	}
	if err := s.FinishBatch(ctx, "b1", "p1.r1", at(61)); !errors.Is(err, catalog.ErrNotOwner) {
		t.Fatalf("the old run finished the batch: %v", err)
	}
	if moved, _ := s.MoveItem(ctx, "b1", "p2.r2", at(62), late, catalog.ItemPending); !moved {
		t.Fatal("the new owner cannot move")
	}
	if err := s.FinishBatch(ctx, "b1", "p2.r2", at(63)); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Batch(ctx, "b1")
	if got.Items[0].State != catalog.ItemDone || got.Items[1].State != catalog.ItemCanceled ||
		!got.FinishedAt.Equal(at(63)) || got.Owner != "p2.r2" {
		t.Fatalf("finished = %+v", got)
	}
	if _, err := s.MoveItem(ctx, "b1", "p2.r2", at(64), late, catalog.ItemDone); !errors.Is(err, catalog.ErrNotOwner) {
		t.Fatalf("an item of a finished batch moved: %v", err)
	}
}

// Concurrent creations cannot pass the bound on unfinished batches together;
// finished batches and other datasources do not count.
func testCreateBatchQuota(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	create(t, s, catalog.Batch{ID: "done", Datasource: "ds", Owner: "p", StartedAt: t0})
	if err := s.FinishBatch(ctx, "done", "p", t0); err != nil {
		t.Fatal(err)
	}
	create(t, s, catalog.Batch{ID: "elsewhere", Datasource: "other", StartedAt: t0})
	var wg sync.WaitGroup
	var created, refused atomic.Int32
	for i := range 8 {
		wg.Go(func() {
			_, ok, err := s.CreateBatch(ctx, catalog.Batch{ID: fmt.Sprint("b", i), Datasource: "ds", StartedAt: t0,
				Items: []catalog.BatchItem{item("a", catalog.ItemPending)}}, 3)
			switch {
			case err == nil && ok:
				created.Add(1)
			case errors.Is(err, catalog.ErrTooManyBatches):
				refused.Add(1)
			default:
				t.Errorf("create: %v %v", ok, err)
			}
		})
	}
	wg.Wait()
	if created.Load() != 3 || refused.Load() != 5 {
		t.Fatalf("created %d, refused %d", created.Load(), refused.Load())
	}
}
