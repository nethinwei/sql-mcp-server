package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/revision"
)

const (
	// eventPollInterval is how often the published revision, and the
	// workspace of each administrator listening, is read while a console
	// listens (publishes from other processes sharing the store arrive this
	// way too), and how often a stream checks its session.
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

// feedState is the published revision and the store's sequence (see
// revision.Store.Sequence): a console learns that revisions changed (a
// draft saved or discarded anywhere) and reads the drafts itself, so a
// change between two reads, or made while it was away, is never missed.
type feedState struct {
	Published publishedEvent
	Sequence  int64
}

// publishedFeed reads the published revision and the newest draft once per
// interval for every listening console, and only while one listens.
type publishedFeed struct {
	bus      *bus.Bus
	store    revision.Store
	interval time.Duration

	mu     sync.Mutex
	last   feedState
	subs   map[chan feedState]struct{}
	cancel context.CancelFunc
}

func newPublishedFeed(b *bus.Bus, store revision.Store, interval time.Duration) *publishedFeed {
	return &publishedFeed{bus: b, store: store, interval: interval, subs: map[chan feedState]struct{}{}}
}

// subscribe returns a channel receiving the state now and on every change,
// and a function to stop.
func (f *publishedFeed) subscribe() (<-chan feedState, func()) {
	ch := make(chan feedState, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs[ch] = struct{}{}
	if f.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		f.cancel = cancel
		f.last = f.read(ctx)
		f.bus.Go("published feed", func(busCtx context.Context) {
			defer context.AfterFunc(busCtx, cancel)() // closing the bus stops the feed
			f.poll(ctx)
		})
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

func (f *publishedFeed) read(ctx context.Context) feedState {
	var out feedState
	if rev, err := f.store.Published(ctx); err == nil {
		out.Published = publishedEvent{ID: strconv.FormatInt(rev.ID, 10), Author: rev.Author}
	}
	if seq, err := f.store.Sequence(ctx); err == nil {
		out.Sequence = seq
	}
	return out
}

func (f *publishedFeed) poll(ctx context.Context) {
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
		if ctx.Err() == nil && next.Published.ID != "" && next != f.last {
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
// administrator or process sharing the store; a revisions event, with the
// newest revision's ID, whenever revisions change after it connected (a
// draft saved anywhere); and a workspace event, with its version, whenever
// the administrator's workspace changes elsewhere (an agent, another
// console). The stream ends when the session does.
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	_, who, ok := h.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	cookie, _ := r.Cookie(cookieName)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	events, stop := h.published.subscribe()
	defer stop()
	workspaces, stopWorkspaces := h.workspaces.subscribe(who.Username)
	defer stopWorkspaces()
	keepAlive := time.NewTicker(eventKeepAlive)
	defer keepAlive.Stop()
	check := time.NewTicker(eventPollInterval)
	defer check.Stop()
	write := eventWriter(w)
	// Send the headers now: before anything is published there is no event,
	// and clients and proxies would wait for the first one.
	w.WriteHeader(http.StatusOK)
	if !write(": connected\n\n") {
		return
	}
	s := stream{workspace: h.workspaceVersion(r.Context(), who.Username)}
	for {
		select {
		case <-r.Context().Done():
			return
		case st := <-events:
			if !s.send(write, st) {
				return
			}
		case <-keepAlive.C:
			if !write(": keep-alive\n\n") {
				return
			}
		case version := <-workspaces:
			if !s.sendWorkspace(write, version) {
				return
			}
		case <-check.C:
			if !h.sessions.alive(cookie.Value) {
				return
			}
		}
	}
}

// eventWriter writes to an event stream and flushes, reporting whether the
// console got it in time.
func eventWriter(w http.ResponseWriter) func(format string, args ...any) bool {
	rc := http.NewResponseController(w)
	return func(format string, args ...any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(eventWriteTimeout))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
}

// stream is what one console was sent.
type stream struct {
	sent    feedState
	started bool
	// workspace is the version of the administrator's workspace last seen.
	workspace int64
}

// workspaceVersion is the version of an administrator's workspace kept on
// the server (0: none, or no store keeps them).
func (h *Handler) workspaceVersion(ctx context.Context, username string) int64 {
	if h.cfg.Plans == nil {
		return 0
	}
	w, _, err := h.cfg.Plans.Workspace(ctx, username)
	if err != nil {
		return 0
	}
	return w.Version
}

// sendWorkspace signals that the administrator's workspace changed (an
// agent applied a plan, another console saved it), with its new version.
func (s *stream) sendWorkspace(write func(string, ...any) bool, version int64) bool {
	if version == 0 || version == s.workspace {
		return true
	}
	s.workspace = version
	return write("event: workspace\ndata: {\"version\":%d}\n\n", version)
}

// send writes what changed in st since what was sent: the published
// revision, and that revisions changed unless the state is the one the
// console found on connecting (it reads the drafts on connecting anyway).
func (s *stream) send(write func(string, ...any) bool, st feedState) bool {
	sent, first := s.sent, !s.started
	s.started = true
	if st.Published.ID != "" && st.Published != sent.Published {
		data, _ := json.Marshal(st.Published)
		if !write("event: published\ndata: %s\n\n", data) {
			return false
		}
	}
	if !first && st.Sequence != sent.Sequence {
		if !write("event: revisions\ndata: {\"sequence\":%d}\n\n", st.Sequence) {
			return false
		}
	}
	s.sent = st
	return true
}
