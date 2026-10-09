package admin

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
)

func TestEventsStreamThePublishedRevision(t *testing.T) {
	f := newFixture(t)
	f.h.published = newPublishedFeed(f.h.cfg.Store, 10*time.Millisecond)
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

// nextEventData is the data of the next event on the stream, within 3s.
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
	case <-time.After(3 * time.Second):
		t.Fatal("no event within 3s")
		return ""
	}
}

// Many consoles listen while revisions are published and others come and
// go: every listener ends on the latest revision, and the feed stops reading
// the store once nobody listens.
func TestEventsUnderManySubscribersAndChurn(t *testing.T) {
	store := revision.NewMemoryStore(nil)
	feed := newPublishedFeed(store, time.Millisecond)
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
					if ev.ID == final {
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
	feed := newPublishedFeed(store, time.Millisecond)
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
			if ev.ID == want {
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
