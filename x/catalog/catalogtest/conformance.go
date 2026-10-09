// Package catalogtest provides the conformance suite every catalog.Store
// implementation must pass, so SQL stores keep the MemoryStore semantics.
package catalogtest

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/catalog"
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
		"ClaimBatches":     testClaimBatches,
		"SaveBatches":      testSaveBatches,
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

func testBatches(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	b := catalog.Batch{ID: "b1", Datasource: "ds", StartedBy: "root", Owner: "p1", StartedAt: t0, Heartbeat: t0,
		Items: []catalog.BatchItem{{Schema: "a", State: "pending"}, {Schema: "b", State: "failed", Error: "boom"}}}
	if err := s.PutBatch(ctx, b); err != nil {
		t.Fatal(err)
	}
	later := b
	later.ID, later.StartedAt = "b2", t0.Add(time.Minute)
	later.FinishedAt = t0.Add(2 * time.Minute)
	_ = s.PutBatch(ctx, later)
	_ = s.PutBatch(ctx, catalog.Batch{ID: "b3", Datasource: "other", StartedAt: t0, Items: []catalog.BatchItem{}})
	got, ok, err := s.Batch(ctx, "b1")
	if err != nil || !ok || !reflect.DeepEqual(got, b) {
		t.Fatalf("batch = %+v %v %v", got, ok, err)
	}
	list, _ := s.Batches(ctx, "ds", 10)
	if len(list) != 2 || list[0].ID != "b2" {
		t.Fatalf("batches = %+v", list)
	}
	if one, _ := s.Batches(ctx, "ds", 1); len(one) != 1 {
		t.Fatalf("limit: %+v", one)
	}
	open, _ := s.UnfinishedBatches(ctx)
	if len(open) != 2 || open[0].ID != "b1" || open[1].ID != "b3" {
		t.Fatalf("unfinished = %+v", open)
	}
	if err := s.CancelBatch(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	b.Items[0].State = "done"
	_ = s.PutBatch(ctx, b) // a progress write after the cancel keeps it
	if got, _, _ := s.Batch(ctx, "b1"); !got.Canceled || got.Items[0].State != "done" {
		t.Fatalf("after cancel = %+v", got)
	}
}

func testClaimBatches(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	_ = s.PutBatch(ctx, catalog.Batch{ID: "b1", Datasource: "ds", Owner: "p1", StartedAt: t0, Heartbeat: t0,
		Items: []catalog.BatchItem{}})
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

// A run that lost its batch cannot write over the new owner's progress, even
// when it writes after the takeover; the owner's writes keep a cancel.
func testSaveBatches(t *testing.T, s catalog.Store) {
	ctx := context.Background()
	item := func(state string) []catalog.BatchItem { return []catalog.BatchItem{{Schema: "a", State: state}} }
	_ = s.PutBatch(ctx, catalog.Batch{ID: "b1", Datasource: "ds", Owner: "p1.r1", StartedAt: t0, Heartbeat: t0,
		Items: item("pending")})
	if ok, err := s.SaveBatch(ctx, catalog.Batch{ID: "b1", Owner: "p1.r1", Heartbeat: t0.Add(time.Second),
		Items: item("running")}); !ok || err != nil {
		t.Fatalf("the owner cannot save: %v, %v", ok, err)
	}
	if ok, _ := s.SaveBatch(ctx, catalog.Batch{ID: "b1", Owner: "p1.r1", Heartbeat: t0.Add(time.Second),
		Items: item("running")}); !ok {
		t.Fatal("the owner cannot save what is already there")
	}
	_ = s.CancelBatch(ctx, "b1")
	if ok, _ := s.ClaimBatch(ctx, "b1", "p2.r2", t0.Add(time.Minute), t0.Add(time.Minute)); !ok {
		t.Fatal("not taken over")
	}
	if ok, _ := s.SaveBatch(ctx, catalog.Batch{ID: "b1", Owner: "p1.r1", Heartbeat: t0.Add(time.Hour),
		Items: item("done"), FinishedAt: t0.Add(time.Hour)}); ok {
		t.Fatal("the old run saved over the new owner")
	}
	got, _, _ := s.Batch(ctx, "b1")
	if got.Owner != "p2.r2" || got.Items[0].State != "running" || !got.FinishedAt.IsZero() || !got.Canceled {
		t.Fatalf("after the old run's write: %+v", got)
	}
	if ok, _ := s.SaveBatch(ctx, catalog.Batch{ID: "b1", Owner: "p2.r2", Heartbeat: t0.Add(time.Hour),
		Items: item("canceled"), FinishedAt: t0.Add(time.Hour)}); !ok {
		t.Fatal("the new owner cannot finish")
	}
	got, _, _ = s.Batch(ctx, "b1")
	if got.Items[0].State != "canceled" || !got.FinishedAt.Equal(t0.Add(time.Hour)) || !got.Canceled {
		t.Fatalf("finished = %+v", got)
	}
	if ok, _ := s.SaveBatch(ctx, catalog.Batch{ID: "b1", Owner: "p2.r2", Heartbeat: t0.Add(2 * time.Hour),
		Items: item("done")}); ok {
		t.Fatal("a finished batch was saved again")
	}
	if ok, _ := s.SaveBatch(ctx, catalog.Batch{ID: "missing", Owner: "p1"}); ok {
		t.Fatal("saved a missing batch")
	}
}
