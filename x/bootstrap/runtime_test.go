package bootstrap

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

// A reload serves the new App at once: a request in flight on the old one
// delays neither the reload nor new requests, and the old App closes once that
// request finishes.
func TestRuntimeReloadPublishesWithoutWaitingForLeases(t *testing.T) {
	oldProvider := &fakeProvider{}
	nextApp := &App{Provider: &fakeProvider{}}
	runtime := NewRuntimeWithBuilder(&App{Provider: oldProvider}, func(string) (*App, error) {
		return nextApp, nil
	})
	_, release, err := runtime.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatal(err)
	}
	got, releaseNext, err := runtime.Acquire()
	if err != nil || got != nextApp {
		t.Fatalf("Acquire during the old lease = %p, %v; want the new app", got, err)
	}
	releaseNext()
	if oldProvider.closed != 0 {
		t.Fatal("the old app closed while a request still used it")
	}
	release()
	runtime.retiring.Wait()
	if oldProvider.closed != 1 {
		t.Fatalf("old provider closed %d times", oldProvider.closed)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeReloadFailureKeepsOldApp(t *testing.T) {
	oldApp := &App{Provider: &fakeProvider{}}
	want := errors.New("invalid replacement")
	runtime := NewRuntimeWithBuilder(oldApp, func(string) (*App, error) {
		return nil, want
	})
	if err := runtime.Reload("ignored"); !errors.Is(err, want) {
		t.Fatalf("Reload error = %v", err)
	}
	if runtime.Current() != oldApp {
		t.Fatal("failed reload replaced the current app")
	}
	_ = runtime.Close()
}

func TestRuntimeReloadPreservesTransactionManager(t *testing.T) {
	manager := tool.NewTransactionManager(time.Minute, 2)
	oldApp := &App{Provider: &fakeProvider{}, Transactions: manager}
	runtime := NewRuntimeWithBuilder(oldApp, func(string) (*App, error) {
		return &App{
			Provider:     &fakeProvider{},
			Transactions: tool.NewTransactionManager(time.Minute, 2),
		}, nil
	})
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatal(err)
	}
	if runtime.Current().Transactions != manager {
		t.Fatal("reload replaced transaction manager")
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeReloadUpdatesTransactionLimits(t *testing.T) {
	oldManager := tool.NewTransactionManager(time.Minute, 2)
	runtime := NewRuntimeWithBuilder(
		&App{Provider: &fakeProvider{}, Transactions: oldManager},
		func(string) (*App, error) {
			return &App{
				Provider: &fakeProvider{}, Transactions: tool.NewTransactionManager(2*time.Minute, 3),
			}, nil
		},
	)
	defer runtime.Close()
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatal(err)
	}
	ttl, maxOpen := runtime.Current().Transactions.Configuration()
	if runtime.Current().Transactions != oldManager || ttl != 2*time.Minute || maxOpen != 3 {
		t.Fatalf("manager kept = %v, limits = %s/%d", runtime.Current().Transactions == oldManager, ttl, maxOpen)
	}
}

func TestRuntimeReloadUpdatesBudgetLimitsAndPreservesState(t *testing.T) {
	oldBudget := budget.New(map[string]budget.Limits{"reader": {MaxSessionCost: 10}}, nil)
	lease, err := oldBudget.Acquire(context.Background(), budget.Scope{Role: "reader", Session: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Complete(budget.Usage{Cost: 6}); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntimeWithBuilder(
		&App{Provider: &fakeProvider{}, Budget: oldBudget},
		func(string) (*App, error) {
			return &App{
				Provider: &fakeProvider{},
				Budget:   budget.New(map[string]budget.Limits{"reader": {MaxSessionCost: 5}}, nil),
			}, nil
		},
	)
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatal(err)
	}
	if runtime.Current().Budget != oldBudget {
		t.Fatal("budget state manager was replaced")
	}
	if _, err := oldBudget.Acquire(context.Background(), budget.Scope{Role: "reader", Session: "s"}); !errors.Is(
		err,
		budget.ErrExceeded,
	) {
		t.Fatalf("updated budget ignored preserved state: %v", err)
	}
	_ = runtime.Close()
}

func TestRuntimeWatchReloadsChangedFile(t *testing.T) {
	file, err := os.CreateTemp("", "runtime-watch-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.WriteString("one"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	reloaded := make(chan struct{}, 1)
	runtime := NewRuntimeWithBuilder(&App{Provider: &fakeProvider{}}, func(string) (*App, error) {
		reloaded <- struct{}{}
		return &App{Provider: &fakeProvider{}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runtime.Watch(ctx, path, 5*time.Millisecond, nil) }()
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("watcher did not reload changed content")
	}
	cancel()
	_ = runtime.Close()
}

func TestRuntimeWatchRetriesUnchangedContentAfterFailure(t *testing.T) {
	file, err := os.CreateTemp("", "runtime-watch-retry-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	defer os.Remove(path)
	_, _ = file.WriteString("one")
	_ = file.Close()
	attempts := make(chan int, 4)
	count := 0
	runtime := NewRuntimeWithBuilder(&App{Provider: &fakeProvider{}}, func(string) (*App, error) {
		count++
		attempts <- count
		if count == 1 {
			return nil, errors.New("transient build failure")
		}
		return &App{Provider: &fakeProvider{}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runtime.Watch(ctx, path, 5*time.Millisecond, nil) }()
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 2; want++ {
		select {
		case got := <-attempts:
			if got != want {
				t.Fatalf("attempt = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing reload attempt %d", want)
		}
	}
	cancel()
	_ = runtime.Close()
}

func BenchmarkRuntimeAcquire(b *testing.B) {
	runtime := NewRuntimeWithBuilder(&App{}, func(string) (*App, error) {
		return &App{}, nil
	})
	b.Cleanup(func() { _ = runtime.Close() })
	b.ReportAllocs()
	for b.Loop() {
		_, release, err := runtime.Acquire()
		if err != nil {
			b.Fatal(err)
		}
		release()
	}
}

// While maxRetiring replaced snapshots still serve requests, a reload is
// refused rather than piling up more; it succeeds once one drains.
func TestRuntimeReloadRefusedWhileEarlierSnapshotsDrain(t *testing.T) {
	runtime := NewRuntimeWithBuilder(&App{Provider: &fakeProvider{}}, func(string) (*App, error) {
		return &App{Provider: &fakeProvider{}}, nil
	})
	defer runtime.Close()
	var releases []func()
	for range maxRetiring {
		_, release, err := runtime.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
		if err := runtime.Reload("ignored"); err != nil {
			t.Fatal(err)
		}
	}
	if err := runtime.Reload("ignored"); !errors.Is(err, ErrReloadBacklog) {
		t.Fatalf("reload with %d snapshots draining = %v", maxRetiring, err)
	}
	releases[0]()
	for runtime.draining.Load() >= maxRetiring {
		time.Sleep(time.Millisecond)
	}
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatalf("reload once one drained: %v", err)
	}
	for _, release := range releases[1:] {
		release()
	}
}

// A transport subscribing while reloads run gets the current App at once and
// then every App published after: none falls between reading the current
// one and subscribing.
func TestRuntimeOnPublishStartsWithTheCurrentApp(t *testing.T) {
	app := func() *App { return &App{Provider: &fakeProvider{}} }
	first, second, third := app(), app(), app()
	next := []*App{second, third}
	runtime := NewRuntimeWithBuilder(first, func(string) (*App, error) {
		app := next[0]
		next = next[1:]
		return app, nil
	})
	defer runtime.Close()
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatal(err)
	}
	var seen []*App
	runtime.OnPublish(func(app *App) { seen = append(seen, app) })
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != second || seen[1] != third {
		t.Fatalf("seen %d apps; want the current one, then the published one", len(seen))
	}
}
