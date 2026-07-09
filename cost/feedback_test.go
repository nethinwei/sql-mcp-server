package cost

import (
	"testing"
)

func TestMemoryStoreAverage(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	s.Record(Feedback{Template: "q1", Rows: 10})
	s.Record(Feedback{Template: "q1", Rows: 20})
	got, ok := s.AverageRows("q1")
	if !ok || got != 15 {
		t.Fatalf("got %d, %v, want 15", got, ok)
	}
	if _, ok := s.AverageRows("missing"); ok {
		t.Fatal("missing template should miss")
	}
}

func TestNoopFeedbackStore(t *testing.T) {
	t.Parallel()
	var s NoopFeedbackStore
	s.Record(Feedback{Template: "q", Rows: 1})
	if _, ok := s.AverageRows("q"); ok {
		t.Fatal("noop should miss")
	}
}
