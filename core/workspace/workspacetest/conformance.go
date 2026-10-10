// Package workspacetest is the conformance suite every workspace.Store
// implementation runs: in memory and on each configuration store database.
package workspacetest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/workspace"
)

// NewStore returns an empty store for one test.
type NewStore func(t *testing.T) workspace.Store

// Run runs the suite.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	for name, fn := range map[string]func(*testing.T, workspace.Store){
		"RoundTrip":       testRoundTrip,
		"VersionChecked":  testVersionChecked,
		"Log":             testLog,
		"ConcurrentWrite": testConcurrentWrite,
	} {
		t.Run(name, func(t *testing.T) { fn(t, newStore(t)) })
	}
}

func sample() workspace.Workspace {
	ref := workspace.Ref{Kind: "role", Key: "analyst"}
	return workspace.Workspace{
		Username: "root", Base: 3,
		Items: workspace.Snapshot{{Ref: ref, Value: map[string]any{"name": "analyst", "description": "读"}}},
		Conflicts: []workspace.Conflict{{ID: "c1", Ref: ref, Path: workspace.Path{{Field: "description"}},
			Source: workspace.Source{Kind: workspace.SourceDraft, ID: "7"}, Pinned: true, Theirs: "draft"}},
		Notes: []workspace.Note{{Source: "agent", By: "laptop", Text: "导入"}},
		Taken: []int64{7}, Checkpoint: 9, CheckpointAt: 4, UpdatedBy: "agent:laptop",
	}
}

// What is written reads back as written, per administrator.
func testRoundTrip(t *testing.T, s workspace.Store) {
	ctx := context.Background()
	if _, ok, err := s.Workspace(ctx, "root"); ok || err != nil {
		t.Fatalf("no workspace yet: %v %v", ok, err)
	}
	want := sample()
	got, err := s.PutWorkspace(ctx, want, 0, workspace.LogEntry{By: "agent:laptop", Op: "apply"})
	if err != nil || got.Version != 1 || got.UpdatedAt.IsZero() {
		t.Fatalf("put = %+v, %v", got, err)
	}
	read, ok, err := s.Workspace(ctx, "root")
	if err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	want.Version, want.UpdatedAt = 1, read.UpdatedAt
	if !reflect.DeepEqual(read, want) {
		t.Fatalf("read = %+v\nwant  %+v", read, want)
	}
	if _, ok, _ := s.Workspace(ctx, "other"); ok {
		t.Fatal("another administrator's workspace")
	}
}

// A write at a version other than the kept one fails, writing nothing.
func testVersionChecked(t *testing.T, s workspace.Store) {
	ctx := context.Background()
	w := sample()
	if _, err := s.PutWorkspace(ctx, w, 0, workspace.LogEntry{Op: "edit"}); err != nil {
		t.Fatal(err)
	}
	w.Base = 4
	if _, err := s.PutWorkspace(ctx, w, 0, workspace.LogEntry{Op: "edit"}); !errors.Is(err, workspace.ErrChanged) {
		t.Fatalf("stale write: %v", err)
	}
	if read, _, _ := s.Workspace(ctx, "root"); read.Base != 3 || read.Version != 1 {
		t.Fatalf("after a stale write = %+v", read)
	}
	if log, _ := s.WorkspaceLog(ctx, "root", 0); len(log) != 1 {
		t.Fatalf("a stale write was logged: %+v", log)
	}
}

// Each write is logged with its version, newest first.
func testLog(t *testing.T, s workspace.Store) {
	ctx := context.Background()
	w := sample()
	refs := []workspace.Ref{{Kind: "role", Key: "analyst"}}
	for i, op := range []string{"edit", "apply", "commit"} {
		entry := workspace.LogEntry{By: "root", Op: op, Items: refs, Note: op}
		if _, err := s.PutWorkspace(ctx, w, int64(i), entry); err != nil {
			t.Fatal(err)
		}
	}
	log, err := s.WorkspaceLog(ctx, "root", 2)
	if err != nil || len(log) != 2 || log[0].Op != "commit" || log[0].Version != 3 || log[1].Op != "apply" ||
		!reflect.DeepEqual(log[0].Items, refs) || log[0].At.IsZero() {
		t.Fatalf("log = %+v, %v", log, err)
	}
	if other, _ := s.WorkspaceLog(ctx, "other", 0); len(other) != 0 {
		t.Fatalf("another administrator's log = %+v", other)
	}
}

// Of writes racing at one version, exactly one succeeds.
func testConcurrentWrite(t *testing.T, s workspace.Store) {
	ctx := context.Background()
	if _, err := s.PutWorkspace(ctx, sample(), 0, workspace.LogEntry{Op: "edit"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.PutWorkspace(ctx, sample(), 1, workspace.LogEntry{Op: "edit"})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, workspace.ErrChanged):
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("%d writes won at one version", won)
	}
}
