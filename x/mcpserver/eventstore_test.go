package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// One session's large results evict its own oldest events, not the events
// another session needs to resume its stream.
func TestFairEventStoreEvictsTheLargestSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newFairEventStore(100)
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
	s := newFairEventStore(10)
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
			for ; s.nBytes+len(data) <= s.maxBytes; i++ {
				session, stream := key(i)
				_ = s.Append(ctx, session, stream, data)
			}
			for b.Loop() {
				session, stream := key(i)
				_ = s.Append(ctx, session, stream, data)
				i++
			}
		})
	}
}
