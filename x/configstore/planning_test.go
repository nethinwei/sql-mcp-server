package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/core/workspace/workspacetest"
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

func TestSQLiteWorkspaceConformance(t *testing.T) {
	workspacetest.Run(t, func(t *testing.T) workspace.Store { return openSQLite(t) })
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
	scope := TokenScope{Datasources: []string{"shop"}}
	laptop, err := s.CreateAdminToken(ctx, AdminToken{ID: "t1", Username: "root", Name: "laptop", TokenHash: "sha256:1",
		ExpiresAt: expires, Scope: scope})
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
	if err != nil || !ok || tok.Username != "root" || !tok.ExpiresAt.Equal(expires) || !tok.LastUsedAt.IsZero() ||
		!reflect.DeepEqual(tok.Scope, scope) {
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
	assertConcurrentPendingPuts(t, s)
}

// Concurrent puts of one key (a recurring template's key is deterministic)
// all succeed and leave one state, one the puts wrote.
func assertConcurrentPendingPuts(t *testing.T, s *SQLStore) {
	t.Helper()
	statuses := []string{"open", "snoozed", "ignored", "open", "snoozed", "ignored", "open", "snoozed"}
	errs := make(chan error, len(statuses))
	var wg sync.WaitGroup
	for i, status := range statuses {
		wg.Go(func() {
			_, err := s.PutPendingState(context.Background(), PendingState{Key: "template:7", Status: status,
				Owner: fmt.Sprint("a", i), UpdatedBy: "root"})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent put: %v", err)
		}
	}
	all, err := s.PendingStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found []PendingState
	for _, p := range all {
		if p.Key == "template:7" {
			found = append(found, p)
		}
	}
	if len(found) != 1 || !slices.Contains(statuses, found[0].Status) {
		t.Fatalf("states of template:7 = %+v", found)
	}
	if log, err := s.PendingLog(context.Background(), "template:7"); err != nil || len(log) == 0 {
		t.Fatalf("log = %+v, %v", log, err)
	}
}

// A save that read a template deleted since does not bring it back; a
// transaction whose function panics is rolled back, releasing the lock.
func TestTemplatesStayDeletedAndPanicsRollBack(t *testing.T) {
	ctx := context.Background()
	s := openSQLite(t)
	first, err := s.PutPlanTemplate(ctx, PlanTemplate{Name: "t", Plan: []byte(`{}`)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePlanTemplate(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	err = s.locked(ctx, func(tx *sql.Tx) error {
		return s.writeTemplate(ctx, tx, PlanTemplate{Name: "t", Plan: []byte(`{}`), Version: first.Version + 1}, true)
	})
	if !errors.Is(err, ErrTemplateConflict) {
		t.Fatalf("a stale save: %v", err)
	}
	if all, _ := s.PlanTemplates(ctx); len(all) != 0 {
		t.Fatalf("templates = %+v", all)
	}
	func() {
		defer func() { _ = recover() }()
		_ = s.locked(ctx, func(*sql.Tx) error { panic("boom") })
	}()
	done := make(chan error, 1)
	go func() {
		_, err := s.PutPlanTemplate(ctx, PlanTemplate{Name: "u", Plan: []byte(`{}`)}, 0)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock of a panicked transaction is still held")
	}
}

// A MySQL store reports matched rows, keeping what its DSN says.
func TestMySQLDSNReportsMatchedRows(t *testing.T) {
	got := mysqlDSN("u:p@tcp(h:3306)/db?parseTime=true&tls=skip-verify")
	for _, want := range []string{"clientFoundRows=true", "parseTime=true", "tls=skip-verify", "u:p@tcp(h:3306)/db"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dsn = %s, missing %s", got, want)
		}
	}
	if bad := mysqlDSN("not a dsn"); bad != "not a dsn" {
		t.Fatalf("a DSN that does not parse became %s", bad)
	}
}
