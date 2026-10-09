package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
)

const (
	// eventPollInterval is how often the published revision is read while a
	// console listens; publishes from other processes sharing the store
	// arrive this way too.
	eventPollInterval = time.Second
	// eventKeepAlive keeps proxies from closing an idle stream.
	eventKeepAlive    = 25 * time.Second
	eventWriteTimeout = 10 * time.Second
)

// publishedEvent is what a console learns about a publish.
type publishedEvent struct {
	ID     string `json:"id"`
	Author string `json:"author,omitempty"`
}

// publishedFeed reads the published revision once per interval for every
// listening console, and only while one listens.
type publishedFeed struct {
	store    revision.Store
	interval time.Duration

	mu     sync.Mutex
	last   publishedEvent
	subs   map[chan publishedEvent]struct{}
	cancel context.CancelFunc
}

func newPublishedFeed(store revision.Store, interval time.Duration) *publishedFeed {
	return &publishedFeed{store: store, interval: interval, subs: map[chan publishedEvent]struct{}{}}
}

// subscribe returns a channel receiving the published revision now and on
// every change, and a function to stop.
func (f *publishedFeed) subscribe() (<-chan publishedEvent, func()) {
	ch := make(chan publishedEvent, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs[ch] = struct{}{}
	if f.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		f.cancel = cancel
		f.last = f.read(ctx)
		go f.poll(ctx)
	}
	ch <- f.last
	return ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.subs, ch)
		if len(f.subs) == 0 && f.cancel != nil {
			f.cancel()
			f.cancel = nil
		}
	}
}

func (f *publishedFeed) read(ctx context.Context) publishedEvent {
	rev, err := f.store.Published(ctx)
	if err != nil {
		return publishedEvent{}
	}
	return publishedEvent{ID: strconv.FormatInt(rev.ID, 10), Author: rev.Author}
}

func (f *publishedFeed) poll(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("published feed panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	tick := time.NewTicker(f.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		next := f.read(ctx)
		f.mu.Lock()
		if ctx.Err() == nil && next.ID != "" && next != f.last {
			f.last = next
			for ch := range f.subs {
				select { // a slow console gets the latest event, not every one
				case <-ch:
				default:
				}
				ch <- next
			}
		}
		f.mu.Unlock()
	}
}

// events streams the published revision to a signed-in console as
// server-sent events: one event on connect and one on every publish, by any
// administrator or process sharing the store. The stream ends when the
// session does.
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	cookie, _ := r.Cookie(cookieName)
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	events, stop := h.published.subscribe()
	defer stop()
	keepAlive := time.NewTicker(eventKeepAlive)
	defer keepAlive.Stop()
	check := time.NewTicker(eventPollInterval)
	defer check.Stop()
	write := func(format string, args ...any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(eventWriteTimeout))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	// Send the headers now: before anything is published there is no event,
	// and clients and proxies would wait for the first one.
	w.WriteHeader(http.StatusOK)
	if !write(": connected\n\n") {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-events:
			data, _ := json.Marshal(ev)
			if ev.ID != "" && !write("event: published\ndata: %s\n\n", data) {
				return
			}
		case <-keepAlive.C:
			if !write(": keep-alive\n\n") {
				return
			}
		case <-check.C:
			if !h.sessions.alive(cookie.Value) {
				return
			}
		}
	}
}
