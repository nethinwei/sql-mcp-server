package mcpserver

import (
	"context"
	"errors"
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
