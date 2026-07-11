package cost

import (
	"sync"
	"time"
)

const defaultMaxFingerprintKeys = 4096

// Feedback records one observed execution for a query template, used to
// calibrate future estimates against reality.
type Feedback struct {
	Template      string
	EstimatedRows int64
	ActualRows    int64
	Rows          int64 // deprecated alias for ActualRows
	Duration      time.Duration
}

// Statistics is a bounded-window summary for one fingerprint.
type Statistics struct {
	Samples       int
	AverageRows   int64
	AverageTime   time.Duration
	LatestRows    int64
	LatestTime    time.Duration
	EstimatedRows int64
}

// FeedbackStore records observed executions and exposes bounded statistics.
type FeedbackStore interface {
	Record(f Feedback)
	Stats(template string) (Statistics, bool)
}

// NoopFeedbackStore discards feedback.
type NoopFeedbackStore struct{}

// Record implements FeedbackStore.
func (NoopFeedbackStore) Record(Feedback) {}

// Stats implements FeedbackStore.
func (NoopFeedbackStore) Stats(string) (Statistics, bool) { return Statistics{}, false }

// MemoryStore keeps a bounded sliding window per fingerprint.
type MemoryStore struct {
	mu                sync.Mutex
	windowSize        int
	anomalyFactor     float64
	anomalyMinSamples int
	invalidator       PlanInvalidator
	maxKeys           int
	m                 map[string][]Feedback
	keys              []string
}

// NewMemoryStore returns an in-memory FeedbackStore.
func NewMemoryStore() *MemoryStore { return NewMemoryStoreWithWindow(32) }

// NewMemoryStoreWithWindow returns a store retaining at most size samples per
// fingerprint.
func NewMemoryStoreWithWindow(size int) *MemoryStore {
	return NewMemoryStoreWithBounds(size, defaultMaxFingerprintKeys)
}

// NewMemoryStoreWithBounds returns a store bounded both per fingerprint and
// across all fingerprints. When full, the oldest inserted fingerprint is
// evicted.
func NewMemoryStoreWithBounds(windowSize, maxKeys int) *MemoryStore {
	return NewAdaptiveMemoryStoreWithBounds(windowSize, maxKeys, 3, 5, nil)
}

// NewAdaptiveMemoryStore additionally invalidates a plan when a new actual-row
// count exceeds the prior window average by factor.
func NewAdaptiveMemoryStore(size int, factor float64, minSamples int, invalidator PlanInvalidator) *MemoryStore {
	return NewAdaptiveMemoryStoreWithBounds(size, defaultMaxFingerprintKeys, factor, minSamples, invalidator)
}

// NewAdaptiveMemoryStoreWithBounds additionally bounds the number of distinct
// fingerprints retained by the adaptive store.
func NewAdaptiveMemoryStoreWithBounds(size, maxKeys int, factor float64, minSamples int, invalidator PlanInvalidator) *MemoryStore {
	if size <= 0 {
		size = 32
	}
	if maxKeys <= 0 {
		maxKeys = defaultMaxFingerprintKeys
	}
	if factor <= 1 {
		factor = 3
	}
	if minSamples <= 0 {
		minSamples = 5
	}
	return &MemoryStore{
		windowSize: size, anomalyFactor: factor, anomalyMinSamples: minSamples,
		invalidator: invalidator, maxKeys: maxKeys, m: map[string][]Feedback{},
	}
}

// Record implements FeedbackStore.
func (s *MemoryStore) Record(f Feedback) {
	s.mu.Lock()
	if f.ActualRows == 0 && f.Rows != 0 {
		f.ActualRows = f.Rows
	}
	previous, exists := s.m[f.Template]
	if !exists {
		if len(s.m) >= s.maxKeys {
			oldest := s.keys[0]
			delete(s.m, oldest)
			s.keys = s.keys[1:]
		}
		s.keys = append(s.keys, f.Template)
	}
	anomalous := false
	if len(previous) >= s.anomalyMinSamples {
		var total int64
		for _, sample := range previous {
			total += sample.ActualRows
		}
		average := total / int64(len(previous))
		anomalous = average > 0 && float64(f.ActualRows) > float64(average)*s.anomalyFactor
	}
	samples := append([]Feedback(nil), previous...)
	samples = append(samples, f)
	if len(samples) > s.windowSize {
		samples = append([]Feedback(nil), samples[len(samples)-s.windowSize:]...)
	}
	s.m[f.Template] = samples
	invalidator := s.invalidator
	s.mu.Unlock()
	if anomalous && invalidator != nil {
		invalidator.InvalidatePlan(f.Template)
	}
}

// AverageRows is retained for source compatibility; new code should use Stats.
func (s *MemoryStore) AverageRows(template string) (int64, bool) {
	stats, ok := s.Stats(template)
	return stats.AverageRows, ok
}

// AverageRows is retained for source compatibility.
func (NoopFeedbackStore) AverageRows(string) (int64, bool) { return 0, false }

// Stats implements FeedbackStore.
func (s *MemoryStore) Stats(template string) (Statistics, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	samples := s.m[template]
	if len(samples) == 0 {
		return Statistics{}, false
	}
	var rows int64
	var duration time.Duration
	for _, sample := range samples {
		rows += sample.ActualRows
		duration += sample.Duration
	}
	latest := samples[len(samples)-1]
	return Statistics{
		Samples:       len(samples),
		AverageRows:   rows / int64(len(samples)),
		AverageTime:   duration / time.Duration(len(samples)),
		LatestRows:    latest.ActualRows,
		LatestTime:    latest.Duration,
		EstimatedRows: latest.EstimatedRows,
	}, true
}

// Anomalous reports a sudden increase over the preceding bounded history.
// At least minSamples historical samples are required to avoid cold-start
// noise. factor values <= 1 use the conservative default of 3.
func (s *MemoryStore) Anomalous(template string, factor float64, minSamples int) bool {
	stats, ok := s.Stats(template)
	if !ok || stats.Samples < minSamples || stats.AverageRows <= 0 {
		return false
	}
	if factor <= 1 {
		factor = 3
	}
	return float64(stats.LatestRows) > float64(stats.AverageRows)*factor
}
