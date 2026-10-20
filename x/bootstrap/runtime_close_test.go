package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/audit"
)

// stuckAuditor never finishes closing until released.
type stuckAuditor struct{ release chan struct{} }

func (stuckAuditor) Record(context.Context, audit.Event) error { return nil }
func (a stuckAuditor) Close()                                  { <-a.release }

// closeWithin runs close and fails the test unless it returns within a
// second, returning its error.
func closeWithin(t *testing.T, close func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- close() }()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("closing outlived its deadline")
		return nil
	}
}

func closeContext(r *Runtime, d time.Duration) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		return r.CloseContext(ctx)
	}
}

func TestRuntimeCloseContextBoundsAStuckClose(t *testing.T) {
	auditor := stuckAuditor{release: make(chan struct{})}
	defer close(auditor.release)
	runtime := NewRuntimeWithBuilder(&App{Provider: &fakeProvider{}, Auditor: auditor}, nil)
	if err := closeWithin(t, closeContext(runtime, 20*time.Millisecond)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error = %v", err)
	}
}

func TestRuntimeCloseContextBoundsALease(t *testing.T) {
	runtime := NewRuntimeWithBuilder(&App{Provider: &fakeProvider{}}, nil)
	_, release, err := runtime.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := closeWithin(t, closeContext(runtime, 20*time.Millisecond)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error = %v", err)
	}
	if runtime.Current() != nil {
		t.Fatal("a closed runtime still serves")
	}
}

// Closing does not wait for a reload's build, and the App the build returns
// afterwards is closed, never served.
func TestRuntimeCloseDuringReloadClosesTheBuiltApp(t *testing.T) {
	building, finish := make(chan struct{}), make(chan struct{})
	built := &fakeProvider{}
	runtime := NewRuntimeWithBuilder(&App{Provider: &fakeProvider{}}, func(string) (*App, error) {
		close(building)
		<-finish
		return &App{Provider: built}, nil
	})
	reloaded := make(chan error, 1)
	go func() { reloaded <- runtime.Reload("ignored") }()
	<-building
	if err := closeWithin(t, closeContext(runtime, time.Second)); err != nil {
		t.Fatal(err)
	}
	close(finish)
	if err := <-reloaded; !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf("reload after close = %v", err)
	}
	if runtime.Current() != nil || built.closed != 1 {
		t.Fatalf("served = %v, built app closed %d times", runtime.Current() != nil, built.closed)
	}
}

// A capability assessment that does not end when canceled bounds closing by
// the context, and the rest is closed all the same.
func TestCloseContextBoundsAStuckAssessment(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{}
	app := &App{Provider: provider, stopAssessing: func() {}, assessed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := closeWithin(t, func() error { return app.CloseContext(ctx) }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error = %v", err)
	}
	if provider.closed == 0 {
		t.Fatal("the provider was left open")
	}
}
