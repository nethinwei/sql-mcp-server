package cost

import (
	"sync"
	"time"
)

// Feedback records one observed execution for a query template, used to
// calibrate future estimates against reality.
type Feedback struct {
	Template string
	Rows     int64
	Duration time.Duration
}

// FeedbackStore records observed executions and answers average-row queries,
// so the Estimate layer can be calibrated against actual behavior over time.
// This is the feedback half of the cost gate's self-correction (P1 wires the
// Estimate layer to consult it).
type FeedbackStore interface {
	Record(f Feedback)
	AverageRows(template string) (rows int64, ok bool)
}

// NoopFeedbackStore discards feedback.
type NoopFeedbackStore struct{}

// Record implements FeedbackStore.
func (NoopFeedbackStore) Record(Feedback) {}

// AverageRows implements FeedbackStore.
func (NoopFeedbackStore) AverageRows(string) (int64, bool) { return 0, false }

type avg struct {
	sum int64
	n   int64
}

// MemoryStore keeps per-template average row counts in memory.
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]avg
}

// NewMemoryStore returns an in-memory FeedbackStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string]avg{}} }

// Record implements FeedbackStore.
func (s *MemoryStore) Record(f Feedback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.m[f.Template]
	a.sum += f.Rows
	a.n++
	s.m[f.Template] = a
}

// AverageRows implements FeedbackStore.
func (s *MemoryStore) AverageRows(template string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[template]
	if !ok || a.n == 0 {
		return 0, false
	}
	return a.sum / a.n, true
}
