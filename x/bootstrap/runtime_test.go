package bootstrap

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/budget"
	"github.com/nethinwei/sql-mcp-server/tool"
)

func TestRuntimeReloadPublishesThenDrainsOldApp(t *testing.T) {
	oldProvider := &fakeProvider{}
	nextProvider := &fakeProvider{}
	oldApp := &App{Provider: oldProvider}
	nextApp := &App{Provider: nextProvider}
	runtime := NewRuntimeWithBuilder(oldApp, func(string) (*App, error) {
		return nextApp, nil
	})
	leased, release, err := runtime.Acquire()
	if err != nil || leased != oldApp {
		t.Fatalf("Acquire = %p, %v", leased, err)
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Reload("ignored") }()
	deadline := time.Now().Add(time.Second)
	for runtime.Current() != nextApp && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runtime.Current() != nextApp {
		t.Fatal("new app was not atomically published")
	}
	select {
	case err := <-done:
		t.Fatalf("reload completed before old lease drained: %v", err)
	default:
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
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

func TestRuntimeReloadRejectsTransactionLimitChanges(t *testing.T) {
	oldManager := tool.NewTransactionManager(time.Minute, 2)
	nextProvider := &fakeProvider{}
	runtime := NewRuntimeWithBuilder(
		&App{Provider: &fakeProvider{}, Transactions: oldManager},
		func(string) (*App, error) {
			return &App{
				Provider: nextProvider, Transactions: tool.NewTransactionManager(2*time.Minute, 3),
			}, nil
		},
	)
	if err := runtime.Reload("ignored"); err == nil {
		t.Fatal("transaction ttl/maxOpen change was accepted")
	}
	if runtime.Current().Transactions != oldManager || nextProvider.closed != 1 {
		t.Fatalf("failed reload changed runtime or leaked replacement: closed=%d", nextProvider.closed)
	}
	_ = runtime.Close()
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
	if _, err := oldBudget.Acquire(context.Background(), budget.Scope{Role: "reader", Session: "s"}); !errors.Is(err, budget.ErrExceeded) {
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
