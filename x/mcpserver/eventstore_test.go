package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bytesStore is a store counting the events' bytes alone, opening each stream
// before its first event as the SDK does.
type bytesStore struct{ *fairEventStore }

func newBytesStore(maxBytes int) bytesStore {
	s := newFairEventStore(maxBytes)
	s.eventCost, s.streamCost = 0, 0
	return bytesStore{s}
}

func (s bytesStore) Append(ctx context.Context, session, stream string, data []byte) error {
	if _, st := s.lookup(session, stream); st == nil {
		_ = s.Open(ctx, session, stream)
	}
	return s.fairEventStore.Append(ctx, session, stream, data)
}

// One session's large results evict its own oldest events, not the events
// another session needs to resume its stream.
func TestFairEventStoreEvictsTheLargestSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newBytesStore(100)
	replay := func(session string, index int) (string, error) {
		var got []string
		for d, err := range s.After(ctx, session, "st", index) {
			if err != nil {
				return "", err
			}
			got = append(got, string(d))
		}
		return strings.Join(got, ","), nil
	}
	for _, d := range []string{"a0", "a1"} {
		_ = s.Append(ctx, "small", "st", []byte(d))
	}
	for i := range 5 {
		_ = s.Append(ctx, "large", "st", []byte(strings.Repeat(string(rune('0'+i)), 40)))
	}
	if got, err := replay("small", -1); err != nil || got != "a0,a1" {
		t.Fatalf("small session replay = %q, %v", got, err)
	}
	if _, err := replay("large", -1); !errors.Is(err, mcp.ErrEventsPurged) {
		t.Fatalf("large session's oldest events must be dropped: %v", err)
	}
	if got, err := replay("large", 3); err != nil || got != strings.Repeat("4", 40) {
		t.Fatalf("latest large event = %q, %v", got, err)
	}
	if s.nBytes > 100 {
		t.Fatalf("store holds %d bytes over its bound", s.nBytes)
	}
	// An event larger than the bound is still kept: it is the latest.
	_ = s.Append(ctx, "small", "st", []byte(strings.Repeat("x", 150)))
	if got, err := replay("small", 1); err != nil || len(got) != 150 {
		t.Fatalf("oversized latest event = %d bytes, %v", len(got), err)
	}
	_ = s.SessionClosed(ctx, "small")
	_ = s.SessionClosed(ctx, "large")
	if s.nBytes != 0 || len(s.sessions) != 0 {
		t.Fatalf("closed sessions left %d bytes in %d sessions", s.nBytes, len(s.sessions))
	}
}

// A stream whose events were all evicted keeps its place: resuming reports
// the events purged, and new events continue its indexes.
func TestFairEventStoreEvictedStreamKeepsItsIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newBytesStore(10)
	_ = s.Append(ctx, "a", "st", []byte("0123456789"))
	_ = s.Append(ctx, "b", "st", []byte("0123456789")) // evicts a's only event
	for _, err := range s.After(ctx, "a", "st", -1) {
		if !errors.Is(err, mcp.ErrEventsPurged) {
			t.Fatalf("evicted stream: %v, want purged", err)
		}
	}
	_ = s.SessionClosed(ctx, "b")
	_ = s.Append(ctx, "a", "st", []byte("x"))
	var got []string
	for d, err := range s.After(ctx, "a", "st", 0) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(d))
	}
	if strings.Join(got, ",") != "x" {
		t.Fatalf("after index 0 = %q, want the second event", got)
	}
	_ = s.SessionClosed(ctx, "a")
	if s.nBytes != 0 || len(s.sessions) != 0 || len(s.bySize) != 0 {
		t.Fatalf("closed sessions left %d bytes, %d sessions, %d in the heap", s.nBytes, len(s.sessions), len(s.bySize))
	}
}

// Each append over the bound evicts one small event while many sessions
// and streams hold many events: spread over 1000 sessions, or all in one
// session's 100 streams.
func BenchmarkFairEventStoreEvictSmallEvents(b *testing.B) {
	for _, shape := range []struct {
		name              string
		sessions, streams int
	}{{"spread", 1000, 10}, {"one session", 1, 100}} {
		b.Run(shape.name, func(b *testing.B) {
			ctx := context.Background()
			s := newFairEventStore(1 << 20)
			data := make([]byte, 64)
			key := func(i int) (string, string) {
				return fmt.Sprint(i % shape.sessions), fmt.Sprint(i / shape.sessions % shape.streams)
			}
			i := 0
			for ; s.nBytes+s.eventCost+len(data) <= s.maxBytes; i++ {
				session, stream := key(i)
				_ = s.Open(ctx, session, stream)
				_ = s.Append(ctx, session, stream, data)
			}
			for b.Loop() {
				session, stream := key(i)
				_ = s.Open(ctx, session, stream) // a no-op on a stream it holds
				_ = s.Append(ctx, session, stream, data)
				i++
			}
		})
	}
}

// What the store holds besides the data counts against its bound: the empty
// priming events and the stream of every request of a long session are
// dropped like events, oldest first, rather than kept until it closes.
func TestFairEventStoreBoundsStreamsAndEmptyEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newFairEventStore(20 * streamCost)
	for i := range 10000 {
		id := fmt.Sprint(i)
		_ = s.Open(ctx, "long", id)
		_ = s.Append(ctx, "long", id, nil) // the SDK's priming event
		_ = s.Append(ctx, "long", id, []byte(`{"jsonrpc":"2.0"}`))
		if s.nBytes > s.maxBytes {
			t.Fatalf("after %d requests the store holds %d, over its bound %d", i+1, s.nBytes, s.maxBytes)
		}
	}
	if n := len(s.sessions["long"].streams); n > 20 {
		t.Fatalf("the session keeps %d streams", n)
	}
	// The latest stream resumes; one dropped long ago is gone.
	var got []string
	for d, err := range s.After(ctx, "long", "9999", 0) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(d))
	}
	if strings.Join(got, ",") != `{"jsonrpc":"2.0"}` {
		t.Fatalf("latest stream after its priming event = %q", got)
	}
	for _, err := range s.After(ctx, "long", "0", 0) {
		if err == nil {
			t.Fatal("a stream dropped long ago replays")
		}
	}
	_ = s.SessionClosed(ctx, "long")
	if s.nBytes != 0 || len(s.sessions) != 0 || len(s.bySize) != 0 {
		t.Fatalf("closed session left %d, %d sessions, %d in the heap", s.nBytes, len(s.sessions), len(s.bySize))
	}
}

// A stream whose entry was dropped while it was still sending is lost: where
// its indexes stand is unknown, so it keeps nothing and every replay of it is
// refused, never answered with events under the wrong indexes.
func TestFairEventStoreLostStreamNeverReplays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newFairEventStore(2 * streamCost)
	_ = s.Open(ctx, "a", "st")
	_ = s.Append(ctx, "a", "st", []byte("first"))
	for i := range 4 { // other requests' streams drop a's event, then its entry
		_ = s.Open(ctx, "b", fmt.Sprint(i))
	}
	if _, st := s.lookup("a", "st"); st != nil {
		t.Fatalf("a's stream kept its entry: %+v", st)
	}
	_ = s.Append(ctx, "a", "st", []byte("second"))
	for _, index := range []int{-1, 0, 1} {
		for d, err := range s.After(ctx, "a", "st", index) {
			if !errors.Is(err, mcp.ErrEventsPurged) {
				t.Fatalf("after %d: %q, %v; want purged", index, d, err)
			}
		}
	}
}

// The latest event of a stream is kept even when it alone is over the bound:
// room is never made by dropping the entry of the stream it goes to.
func TestFairEventStoreKeepsTheLatestEventOverTheBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newFairEventStore(streamCost + eventCost)
	_ = s.Open(ctx, "a", "st")
	_ = s.Append(ctx, "a", "st", []byte(strings.Repeat("x", 4*streamCost)))
	for d, err := range s.After(ctx, "a", "st", -1) {
		if err != nil || len(d) != 4*streamCost {
			t.Fatalf("oversized latest event = %d bytes, %v", len(d), err)
		}
	}
}
