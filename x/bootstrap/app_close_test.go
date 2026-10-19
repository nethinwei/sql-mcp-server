package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/engine"
)

// A call still running when the drain times out audits after the auditor is
// closed: the record is refused, the process does not panic.
func TestAppCloseContextAuditsInFlightCallAfterDrainTimeout(t *testing.T) {
	eng, _ := engine.New(engine.WithIOPool(1), engine.WithMaxInflight(2))
	auditor := audit.NewAsyncAuditorWithClose(func(audit.Event) error { return nil }, nil, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	recorded := make(chan error, 1)
	go func() {
		_, _ = engine.Submit(context.Background(), eng, "", func(ctx context.Context) (any, error) {
			close(started)
			<-release
			recorded <- auditor.Record(ctx, audit.Event{Tool: "read_records"})
			return nil, nil
		})
	}()
	<-started
	app := &App{Provider: &fakeProvider{}, Engine: eng, Auditor: auditor}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := app.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext error = %v", err)
	}
	close(release)
	if err := <-recorded; !errors.Is(err, audit.ErrSinkClosed) {
		t.Fatalf("Record after close = %v, want ErrSinkClosed", err)
	}
}
