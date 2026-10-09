package configstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/catalog"
	"github.com/nethinwei/sql-mcp-server/x/catalog/catalogtest"
)

func openSQLite(t *testing.T) *SQLStore {
	t.Helper()
	spec := sqliteSpec(t)
	if err := Init(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestSQLiteCatalogConformance(t *testing.T) {
	catalogtest.Run(t, func(t *testing.T) catalog.Store { return openSQLite(t) })
}

func TestSQLitePlanning(t *testing.T) {
	assertPlanning(t, openSQLite(t))
}

// assertPlanning checks plan templates, admin tokens and pending states on a
// store.
func assertPlanning(t *testing.T, s *SQLStore) {
	t.Helper()
	assertTemplates(t, s)
	assertTokens(t, s)
	assertPending(t, s)
}

func assertTemplates(t *testing.T, s *SQLStore) {
	t.Helper()
	ctx := context.Background()
	if all, err := s.PlanTemplates(ctx); err != nil || len(all) != 0 {
		t.Fatalf("templates = %v, %v", all, err)
	}
	first, err := s.PutPlanTemplate(ctx, PlanTemplate{Name: "b", Plan: []byte(`{"x":1}`), Author: "root"}, 0)
	if err != nil || first.Version != 1 {
		t.Fatalf("create = %+v, %v", first, err)
	}
	if _, err := s.PutPlanTemplate(ctx, PlanTemplate{Name: "b", Plan: []byte(`{}`)}, 0); !errors.Is(
		err,
		ErrTemplateExists,
	) {
		t.Fatalf("creating a taken name: %v", err)
	}
	second, err := s.PutPlanTemplate(ctx, PlanTemplate{Name: "b", Plan: []byte(`{"x":3}`), Author: "other"}, 1)
	if err != nil || second.Version != 2 {
		t.Fatalf("update = %+v, %v", second, err)
	}
	if _, err := s.PutPlanTemplate(ctx, PlanTemplate{Name: "b", Plan: []byte(`{}`)}, 1); !errors.Is(
		err,
		ErrTemplateConflict,
	) {
		t.Fatalf("updating a stale version: %v", err)
	}
	if _, err := s.PutPlanTemplate(ctx, PlanTemplate{Name: "a", Recurring: true, Plan: []byte(`{}`)}, 0); err != nil {
		t.Fatal(err)
	}
	all, err := s.PlanTemplates(ctx)
	if err != nil || len(all) != 2 || all[0].Name != "a" || !all[0].Recurring || string(all[1].Plan) != `{"x":3}` ||
		all[1].Author != "other" || all[1].UpdatedAt.IsZero() {
		t.Fatalf("templates = %+v, %v", all, err)
	}
	versions, err := s.PlanTemplateVersions(ctx, "b")
	if err != nil || len(versions) != 2 || versions[0].Version != 2 || string(versions[1].Plan) != `{"x":1}` {
		t.Fatalf("versions = %+v, %v", versions, err)
	}
	if err := s.DeletePlanTemplate(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePlanTemplate(ctx, "b"); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
	if v, _ := s.PlanTemplateVersions(ctx, "b"); len(v) != 0 {
		t.Fatalf("versions of a deleted template remain: %+v", v)
	}
}

func assertTokens(t *testing.T, s *SQLStore) {
	t.Helper()
	ctx := context.Background()
	expires := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	laptop, err := s.CreateAdminToken(ctx, AdminToken{ID: "t1", Username: "root", Name: "laptop", TokenHash: "sha256:1",
		ExpiresAt: expires})
	if err != nil || laptop.CreatedAt.IsZero() {
		t.Fatalf("create = %+v, %v", laptop, err)
	}
	token := func(id, user, name string) error {
		_, err := s.CreateAdminToken(ctx, AdminToken{ID: id, Username: user, Name: name, TokenHash: "sha256:" + id})
		return err
	}
	if err := token("t2", "root", "laptop"); !errors.Is(err, ErrTokenNameTaken) {
		t.Fatalf("a taken name: %v", err)
	}
	if err := token("t3", "root", "ci"); err != nil {
		t.Fatal(err)
	}
	if err := token("t4", "other", "laptop"); err != nil {
		t.Fatal(err)
	}
	tok, ok, err := s.AdminTokenByHash(ctx, "sha256:1")
	if err != nil || !ok || tok.Username != "root" || !tok.ExpiresAt.Equal(expires) || !tok.LastUsedAt.IsZero() {
		t.Fatalf("by hash = %+v %v %v", tok, ok, err)
	}
	used := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := s.TouchAdminToken(ctx, "t1", used); err != nil {
		t.Fatal(err)
	}
	mine, err := s.AdminTokens(ctx, "root")
	if err != nil || len(mine) != 2 || !mine[0].LastUsedAt.Equal(used) {
		t.Fatalf("tokens = %+v, %v", mine, err)
	}
	if err := s.DeleteAdminToken(ctx, "other", "t1"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("revoking another administrator's token: %v", err)
	}
	if err := s.DeleteAdminToken(ctx, "root", "t1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.AdminTokenByHash(ctx, "sha256:1"); ok {
		t.Fatal("a revoked token still authenticates")
	}
}

func assertPending(t *testing.T, s *SQLStore) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.PutPendingState(ctx, PendingState{Key: "drift:shop.a.b", Status: "snoozed", Owner: "root",
		Until: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedBy: "root"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutPendingState(ctx, PendingState{Key: "drift:shop.a.b", Status: "ignored", Reason: "历史表",
		UpdatedBy: "other"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.PendingStates(ctx)
	if err != nil || len(all) != 1 || all[0].Status != "ignored" || all[0].Reason != "历史表" {
		t.Fatalf("states = %+v, %v", all, err)
	}
	log, err := s.PendingLog(ctx, "drift:shop.a.b")
	if err != nil || len(log) != 2 || log[0].Status != "ignored" || log[1].Owner != "root" {
		t.Fatalf("log = %+v, %v", log, err)
	}
}
