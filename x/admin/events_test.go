package admin

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

func TestEventsStreamThePublishedRevision(t *testing.T) {
	f := newFixture(t)
	f.h.published = newPublishedFeed(nil, f.h.cfg.Store, 10*time.Millisecond)
	ctx := context.Background()
	publish := func() {
		current, _ := f.h.cfg.Store.Published(ctx)
		d, err := f.h.cfg.Store.Create(ctx, revision.Draft{ParentID: current.ID, Payload: []byte("x: 1\n"), Author: "other"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.h.cfg.Store.Publish(ctx, d.ID, current.ID, revision.Meta{}); err != nil {
			t.Fatal(err)
		}
	}
	publish()
	srv := httptest.NewServer(f.h)
	defer srv.Close()

	if res, err := http.Get(srv.URL + "/admin/events"); err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed-out stream = %v %v", res, err)
	}
	_, cookie, _ := f.login(t, "correct horse battery")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/admin/events", nil)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("stream = %v %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	lines := bufio.NewScanner(res.Body)
	next := func() string { return nextEventData(t, lines) }
	if got := next(); got != `{"id":"1","author":"other"}` {
		t.Fatalf("first event = %s, want the published revision", got)
	}
	publish()
	if got := next(); got != `{"id":"2","author":"other"}` {
		t.Fatalf("event after a publish = %s", got)
	}
}

// Revisions saved or discarded after a console connected, by another
// console, are signaled with the store's sequence; the console then reads
// the drafts, so none between two signals is missed.
func TestEventsSignalNewRevisions(t *testing.T) {
	f := newFixture(t)
	f.h.published = newPublishedFeed(nil, f.h.cfg.Store, 10*time.Millisecond)
	ctx := context.Background()
	draft := func() int64 {
		current, _ := f.h.cfg.Store.Published(ctx)
		d, err := f.h.cfg.Store.Create(ctx, revision.Draft{ParentID: current.ID, Payload: []byte("x: 1\n"), Author: "root"})
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	publishNext(t, f.h.cfg.Store)
	old := draft()
	srv := httptest.NewServer(f.h)
	defer srv.Close()
	_, cookie, _ := f.login(t, "correct horse battery")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/admin/events", nil)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("stream = %v %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	lines := bufio.NewScanner(res.Body)
	if got := nextEventData(t, lines); !strings.Contains(got, `"id":"1"`) {
		t.Fatalf("first event = %s, want the published revision", got)
	}
	for _, change := range []func(){
		func() { draft(); draft() },
		func() { _, _ = f.h.cfg.Store.Discard(ctx, old) }, // an older draft: the newest ID stays
	} {
		change()
		seq, _ := f.h.cfg.Store.Sequence(ctx)
		want := `{"sequence":` + strconv.FormatInt(seq, 10) + `}`
		for deadline := time.Now().Add(3 * time.Second); ; {
			got := nextEventData(t, lines)
			if got == want {
				break
			}
			if time.Now().After(deadline) || !strings.HasPrefix(got, `{"sequence":`) {
				t.Fatalf("event = %s, want %s eventually", got, want)
			}
		}
	}
}

// A change of the signed-in administrator's workspace made elsewhere (an
// agent applying a plan, another console) is signaled with its version; the
// workspace as it was on connecting is not.
func TestEventsSignalWorkspaceChanges(t *testing.T) {
	f := newFixture(t)
	spec := configstore.Spec{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "store.db")}
	if err := configstore.Init(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	store, err := configstore.Open(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.h = New(Config{Store: f.h.cfg.Store, Accounts: f.accounts, Plans: store, Now: f.h.cfg.Now})
	ctx := context.Background()
	publishNext(t, f.h.cfg.Store)
	w, err := store.PutWorkspace(ctx, workspace.Workspace{Username: "root", Base: 1}, 0, workspace.LogEntry{Op: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.h)
	defer srv.Close()
	_, cookie, _ := f.login(t, "correct horse battery")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/admin/events", nil)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("stream = %v %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	lines := bufio.NewScanner(res.Body)
	if got := nextEventData(t, lines); !strings.Contains(got, `"id":"1"`) {
		t.Fatalf("first event = %s, want the published revision", got)
	}
	w.UpdatedBy = "agent:laptop"
	if _, err := store.PutWorkspace(ctx, w, w.Version, workspace.LogEntry{Op: "apply"}); err != nil {
		t.Fatal(err)
	}
	if got := nextEventData(t, lines); got != `{"version":2}` {
		t.Fatalf("event = %s, want the workspace's new version", got)
	}
}

// nextEventData is the data of the next event on the stream, within 10s
// (the stream checks every second; a loaded race run is slow).
func nextEventData(t *testing.T, lines *bufio.Scanner) string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		for lines.Scan() {
			if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
				done <- data
				return
			}
		}
		done <- ""
	}()
	select {
	case data := <-done:
		return data
	case <-time.After(10 * time.Second):
		t.Fatal("no event within 10s")
		return ""
	}
}

// Many consoles listen while revisions are published and others come and
// go: every listener ends on the latest revision, and the feed stops reading
// the store once nobody listens.
func TestEventsUnderManySubscribersAndChurn(t *testing.T) {
	store := revision.NewMemoryStore(nil)
	feed := newPublishedFeed(nil, store, time.Millisecond)
	publish := func() { publishNext(t, store) }
	publish()
	const listeners, publishes = 50, 30
	final := strconv.FormatInt(int64(1+publishes), 10)
	var wg sync.WaitGroup
	for range listeners {
		events, stop := feed.subscribe()
		wg.Go(func() {
			defer stop()
			deadline := time.After(5 * time.Second)
			for {
				select {
				case ev := <-events:
					if ev.Published.ID == final {
						return
					}
				case <-deadline:
					t.Error("a listener never saw the latest revision")
					return
				}
			}
		})
	}
	for range listeners {
		wg.Go(func() { // consoles opening and closing the page
			for range 20 {
				_, stop := feed.subscribe()
				stop()
			}
		})
	}
	for range publishes {
		publish()
		time.Sleep(time.Millisecond)
	}
	wg.Wait()
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if len(feed.subs) != 0 || feed.cancel != nil {
		t.Fatalf("after every listener left: %d subscribers, polling = %v", len(feed.subs), feed.cancel != nil)
	}
}

// A listener that does not read keeps only the latest event.
func TestEventsSlowListenerGetsTheLatest(t *testing.T) {
	store := revision.NewMemoryStore(nil)
	feed := newPublishedFeed(nil, store, time.Millisecond)
	events, stop := feed.subscribe()
	defer stop()
	<-events // nothing published yet
	var last int64
	for range 5 {
		last = publishNext(t, store)
	}
	want := strconv.FormatInt(last, 10)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Published.ID == want {
				return
			}
		case <-deadline:
			t.Fatal("the slow listener did not get the latest revision")
		}
	}
}

// The stream ends when its session does.
func TestEventsEndWithTheSession(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.h)
	defer srv.Close()
	_, cookie, csrf := f.login(t, "correct horse battery")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/events", nil)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	f.do(http.MethodPost, "/admin/logout", "", map[string]string{csrfHeader: csrf}, cookie)
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, res.Body); done <- err }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived its session")
	}
}

// publishNext publishes a new revision on top of the published one.
func publishNext(t *testing.T, store revision.Store) int64 {
	t.Helper()
	ctx := context.Background()
	current, _ := store.Published(ctx)
	d, err := store.Create(ctx, revision.Draft{ParentID: current.ID, Payload: []byte("x\n")})
	if err != nil {
		t.Error(err)
		return 0
	}
	p, err := store.Publish(ctx, d.ID, current.ID, revision.Meta{})
	if err != nil {
		t.Error(err)
	}
	return p.ID
}

// Consoles of one administrator share one read of its workspace per
// interval, each learning a change; the feed stops reading once nobody
// listens.
func TestWorkspaceFeedSharesReadsPerAdministrator(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	reads, version := map[string]int{}, int64(1)
	feed := newWorkspaceFeed(nil, func(_ context.Context, username string) int64 {
		mu.Lock()
		defer mu.Unlock()
		reads[username]++
		return version
	}, 5*time.Millisecond)
	start := time.Now()
	var chans []<-chan int64
	var stops []func()
	for range 5 {
		ch, stop := feed.subscribe("root")
		chans, stops = append(chans, ch), append(stops, stop)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	version = 2 // a change elsewhere
	mu.Unlock()
	for i, ch := range chans {
		deadline := time.After(3 * time.Second)
		for got := int64(0); got != 2; {
			select {
			case got = <-ch:
			case <-deadline:
				t.Fatalf("console %d did not learn version 2", i)
			}
		}
	}
	for _, stop := range stops {
		stop()
	}
	elapsed := time.Since(start)
	mu.Lock()
	// One read per tick in all (a ticker fires at most once an interval),
	// not one per console.
	if n, most := reads["root"], int(elapsed/(5*time.Millisecond))+1; n == 0 || n > most {
		t.Errorf("%d reads in %v, at most %d", n, elapsed, most)
	}
	mu.Unlock()
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if len(feed.subs) != 0 || len(feed.last) != 0 || feed.cancel != nil {
		t.Fatalf("after every console left: %d administrators, polling = %v", len(feed.subs), feed.cancel != nil)
	}
}

// Without workspaces kept there is nothing to listen to.
func TestNoWorkspaceFeed(t *testing.T) {
	t.Parallel()
	if ch, stop := (*workspaceFeed)(nil).subscribe("root"); ch != nil {
		t.Fatal("a nil feed sends")
	} else {
		stop()
	}
}
