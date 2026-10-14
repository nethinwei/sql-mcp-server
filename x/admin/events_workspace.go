package admin

import (
	"context"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/bus"
)

// workspaceFeed reads, once per interval and only while a console listens,
// the workspace version of each administrator with a listening console:
// consoles of one administrator share one read.
type workspaceFeed struct {
	bus      *bus.Bus
	read     func(ctx context.Context, username string) int64
	interval time.Duration

	mu     sync.Mutex
	last   map[string]int64
	subs   map[string]map[chan int64]struct{}
	cancel context.CancelFunc
}

func newWorkspaceFeed(
	b *bus.Bus, read func(context.Context, string) int64, interval time.Duration,
) *workspaceFeed {
	return &workspaceFeed{
		bus: b, read: read, interval: interval, last: map[string]int64{},
		subs: map[string]map[chan int64]struct{}{},
	}
}

// subscribe returns a channel receiving username's workspace version on
// every change, and a function to stop. A nil feed (no workspaces kept)
// never sends.
func (f *workspaceFeed) subscribe(username string) (<-chan int64, func()) {
	if f == nil {
		return nil, func() {}
	}
	ch := make(chan int64, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs[username] == nil {
		f.subs[username] = map[chan int64]struct{}{}
	}
	f.subs[username][ch] = struct{}{}
	if f.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		f.cancel = cancel
		f.bus.Go("workspace feed", func(busCtx context.Context) {
			defer context.AfterFunc(busCtx, cancel)() // closing the bus stops the feed
			f.poll(ctx)
		})
	}
	return ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.subs[username], ch)
		if len(f.subs[username]) == 0 {
			delete(f.subs, username)
			delete(f.last, username)
		}
		if len(f.subs) == 0 && f.cancel != nil {
			f.cancel()
			f.cancel = nil
		}
	}
}

func (f *workspaceFeed) poll(ctx context.Context) {
	tick := time.NewTicker(f.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		f.mu.Lock()
		usernames := make([]string, 0, len(f.subs))
		for username := range f.subs {
			usernames = append(usernames, username)
		}
		f.mu.Unlock()
		for _, username := range usernames {
			f.publish(ctx, username, f.read(ctx, username))
		}
	}
}

// publish sends version to username's listeners when it changed.
func (f *workspaceFeed) publish(ctx context.Context, username string, version int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	subs := f.subs[username]
	if ctx.Err() != nil || len(subs) == 0 || version == f.last[username] {
		return
	}
	f.last[username] = version
	for ch := range subs {
		select { // a slow console gets the latest version, not every one
		case <-ch:
		default:
		}
		ch <- version
	}
}
