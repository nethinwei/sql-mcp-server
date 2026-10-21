package workspaceops

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/workspace"
)

// Operations asking at once for a revision's items decode it once.
func TestItemsCacheBuildsOnce(t *testing.T) {
	t.Parallel()
	var c ItemsCache
	var builds atomic.Int32
	release := make(chan struct{})
	build := func() (workspace.Snapshot, error) {
		builds.Add(1)
		<-release
		return workspace.Snapshot{}, nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Items(1, build); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond) // all ask while the first decodes
	close(release)
	wg.Wait()
	if n := builds.Load(); n != 1 {
		t.Fatalf("decoded %d times", n)
	}
}
