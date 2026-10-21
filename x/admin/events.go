package admin

import (
	"context"
	"encoding/json"
	"errors"
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
	// eventAccountChecks is every how many session checks a stream re-reads
	// the session's account (disabled, or its password changed).
	eventAccountChecks = 30
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

	mu   sync.Mutex
	last feedState
	// ready: last was read (by the poll the first listener started).
	ready  bool
	subs   map[chan feedState]struct{}
	cancel context.CancelFunc
}

func newPublishedFeed(b *bus.Bus, store revision.Store, interval time.Duration) *publishedFeed {
	return &publishedFeed{bus: b, store: store, interval: interval, subs: map[chan feedState]struct{}{}}
}

// subscribe returns a channel receiving the state now (once read) and on
// every change, and a function to stop. The store is read by the poll, never
// under f.mu: a store that hangs holds up no other listener.
func (f *publishedFeed) subscribe() (<-chan feedState, func()) {
	ch := make(chan feedState, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs[ch] = struct{}{}
	if f.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		f.cancel, f.ready = cancel, false
		f.bus.Go("published feed", func(busCtx context.Context) {
			defer context.AfterFunc(busCtx, cancel)() // closing the bus stops the feed
			f.poll(ctx)
		})
	}
	if f.ready {
		ch <- f.last
	}
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
	if rev, err := f.store.PublishedSummary(ctx); err == nil { // its ID and author: not its payload
		out.Published = publishedEvent{ID: strconv.FormatInt(rev.ID, 10), Author: rev.Author}
	}
	if seq, err := f.store.Sequence(ctx); err == nil {
		out.Sequence = seq
	}
	return out
}

// poll reads the state at once, for the listeners waiting for it, then
// once per interval.
func (f *publishedFeed) poll(ctx context.Context) {
	tick := time.NewTicker(f.interval)
	defer tick.Stop()
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
		next := f.read(ctx)
		f.mu.Lock()
		if ctx.Err() == nil && (first || next.Published.ID != "" && next != f.last) {
			f.last, f.ready = next, true
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
// console). The stream ends when the session does, and when the server
// shuts down (EndStreams).
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	sess, who, err := h.session(r)
	if err != nil {
		refuseSession(w, err)
		return
	}
	cookie, _ := r.Cookie(cookieName)
	held := h.sessionHeld(r.Context(), cookie.Value, sess)
	eventStreamHeaders(w.Header())
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
		case <-h.ending:
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
			if !held() {
				return
			}
		}
	}
}

// eventStreamHeaders are the headers of an event stream: not stored, nor
// buffered by a proxy.
func eventStreamHeaders(h http.Header) {
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
}

// sessionHeld reports, at each check of a stream, whether its session id
// (sess) lasts: it has not expired, and at every eventAccountChecks-th check
// its account is read again, as a request would. An account that cannot be
// read now does not end it.
func (h *Handler) sessionHeld(ctx context.Context, id string, sess session) func() bool {
	checks := 0
	return func() bool {
		if !h.sessions.alive(id) {
			return false
		}
		if checks++; checks%eventAccountChecks != 0 {
			return true
		}
		if _, err := h.holder(ctx, sess); errors.Is(err, errSignedOut) {
			h.sessions.delete(id)
			return false
		}
		return true
	}
}

// EndStreams ends the event streams: the server is shutting down, and would
// otherwise wait for the consoles listening to leave.
func (h *Handler) EndStreams() { h.endStreams.Do(func() { close(h.ending) }) }

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
