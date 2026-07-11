package audit

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/cost"
)

func TestAsyncAuditorRecordsToSink(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []Event
	sink := func(e Event) error {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
		return nil
	}
	a := NewAsyncAuditor(sink, 8)
	_ = a.Record(context.Background(), Event{Tool: "read_records", Role: "reader", Cost: &cost.Plan{}})
	a.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Tool != "read_records" {
		t.Fatalf("sink got %v", got)
	}
}

func TestAsyncAuditorDropsWhenFull(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	sink := func(_ Event) error { <-block; return nil } // block flusher
	a := NewAsyncAuditor(sink, 2)
	_ = a.Record(context.Background(), Event{Tool: "x"})
	_ = a.Record(context.Background(), Event{Tool: "x"}) // queue full now
	// queue full; next records should drop
	for i := 0; i < 5; i++ {
		_ = a.Record(context.Background(), Event{Tool: "drop"})
	}
	if a.Dropped() == 0 {
		t.Fatal("expected drops on full queue")
	}
	close(block)
	a.Close()
}

func TestNoopAuditor(t *testing.T) {
	t.Parallel()
	n := NoopAuditor{}
	if err := n.Record(context.Background(), Event{}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordNonBlocking(t *testing.T) {
	t.Parallel()
	a := NewAsyncAuditor(nil, 1)
	start := time.Now()
	_ = a.Record(context.Background(), Event{})
	_ = a.Record(context.Background(), Event{}) // dropped, not blocked
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("Record blocked")
	}
	a.Close()
}

// TestEventJSONGolden freezes the audit JSON Lines schema. The field names
// below are a public contract (docs/tool-contract.md): adding an optional
// field is compatible; renaming or removing one is breaking and must not
// happen silently.
func TestEventJSONGolden(t *testing.T) {
	t.Parallel()
	e := Event{
		Time:          time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		DecisionID:    "0123456789abcdef0123456789abcdef",
		Role:          "reader",
		Entity:        "users",
		Action:        "read",
		Tool:          "read_records",
		Input:         []byte(`{"entity":"users"}`),
		ResultSummary: "2 rows",
		Allowed:       false,
		Code:          "UNAUTHORIZED",
		Error:         "tool: unauthorized",
		Duration:      1500 * time.Millisecond,
		ReturnedRows:  2,
	}
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"time":"2026-01-02T03:04:05Z",` +
		`"decisionId":"0123456789abcdef0123456789abcdef",` +
		`"role":"reader","entity":"users","action":"read",` +
		`"tool":"read_records","input":{"entity":"users"},` +
		`"resultSummary":"2 rows","allowed":false,` +
		`"code":"UNAUTHORIZED","error":"tool: unauthorized",` +
		`"returnedRows":2,"durationMs":1500}`
	if string(got) != want {
		t.Fatalf("audit event JSON drifted from the frozen schema\n got: %s\nwant: %s", got, want)
	}
}

// TestEventJSONOmitsEmptyOptionalFields ensures optional fields stay omitted
// so successful minimal events remain compact and stable.
func TestEventJSONOmitsEmptyOptionalFields(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(Event{
		Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Tool: "read_records", Allowed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"time":"2026-01-02T03:04:05Z","tool":"read_records",` +
		`"allowed":true,"returnedRows":0,"durationMs":0}`
	if string(got) != want {
		t.Fatalf("minimal audit event JSON = %s, want %s", got, want)
	}
}

func TestRedactInput(t *testing.T) {
	input := []byte(`{"transaction":"secret-token","filter":[` +
		`{"field":"email","op":"eq","value":"alice@example.com"}],` +
		`"values":{"email":"bob@example.com","name":"Bob"}}`)
	got := string(RedactInput(input, map[string]bool{"email": true}))
	for _, secret := range []string{"secret-token", "alice@example.com", "bob@example.com"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted input contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"name":"Bob"`) {
		t.Fatalf("non-sensitive value was removed: %s", got)
	}
}

func TestFileSinkPersistsAndCloses(t *testing.T) {
	path := t.TempDir() + "/audit.jsonl"
	sink, err := OpenFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Record(Event{Tool: "read_records"}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"tool":"read_records"`) {
		t.Fatalf("audit file = %s", data)
	}
	if err := sink.Record(Event{}); err != ErrSinkClosed {
		t.Fatalf("record after close error = %v", err)
	}
}
