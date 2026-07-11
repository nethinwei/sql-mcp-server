package cost

import (
	"context"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/codegen"
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

func TestMemoryStoreBoundedStatistics(t *testing.T) {
	s := NewMemoryStoreWithWindow(2)
	s.Record(Feedback{Template: "fp", EstimatedRows: 1, ActualRows: 10, Duration: time.Second})
	s.Record(Feedback{Template: "fp", EstimatedRows: 2, ActualRows: 20, Duration: 2 * time.Second})
	s.Record(Feedback{Template: "fp", EstimatedRows: 3, ActualRows: 30, Duration: 3 * time.Second})
	stats, ok := s.Stats("fp")
	if !ok || stats.Samples != 2 || stats.AverageRows != 25 || stats.EstimatedRows != 3 {
		t.Fatalf("stats = %+v, ok = %v", stats, ok)
	}
}

func TestMemoryStoreBoundsFingerprintKeysFIFO(t *testing.T) {
	s := NewMemoryStoreWithBounds(2, 2)
	s.Record(Feedback{Template: "first", ActualRows: 1})
	s.Record(Feedback{Template: "second", ActualRows: 2})
	s.Record(Feedback{Template: "first", ActualRows: 3})
	s.Record(Feedback{Template: "third", ActualRows: 4})

	if _, ok := s.Stats("first"); ok {
		t.Fatal("oldest fingerprint was not evicted")
	}
	if _, ok := s.Stats("second"); !ok {
		t.Fatal("second fingerprint should remain")
	}
	if _, ok := s.Stats("third"); !ok {
		t.Fatal("new fingerprint should remain")
	}
	if got := len(s.m); got != 2 {
		t.Fatalf("fingerprint keys = %d, want 2", got)
	}
}

func TestFingerprintStableAcrossValuesAndSensitiveToTypes(t *testing.T) {
	a := Fingerprint("primary", "postgres", codegen.Compiled{SQL: " SELECT  *  FROM t WHERE id=$1 ", Args: []any{int64(1)}})
	b := Fingerprint("primary", "postgres", codegen.Compiled{SQL: "SELECT * FROM t WHERE id=$1", Args: []any{int64(2)}})
	c := Fingerprint("primary", "postgres", codegen.Compiled{SQL: "SELECT * FROM t WHERE id=$1", Args: []any{"2"}})
	if a != b {
		t.Fatal("values must not affect fingerprint")
	}
	if a == c {
		t.Fatal("parameter types must affect fingerprint")
	}
	if a == Fingerprint("replica", "postgres", codegen.Compiled{SQL: "SELECT * FROM t WHERE id=$1", Args: []any{int64(2)}}) {
		t.Fatal("datasources must not share fingerprints")
	}
	if len(a) < len("fp:v2:") || a[:len("fp:v2:")] != "fp:v2:" {
		t.Fatalf("fingerprint version = %q, want v2", a)
	}
}

type recordingInvalidator struct{ keys []string }

func (r *recordingInvalidator) InvalidatePlan(key string) { r.keys = append(r.keys, key) }

func TestFeedbackAnomalyInvalidatesPlan(t *testing.T) {
	invalidator := &recordingInvalidator{}
	store := NewAdaptiveMemoryStore(4, 2, 2, invalidator)
	store.Record(Feedback{Template: "fp", ActualRows: 10})
	store.Record(Feedback{Template: "fp", ActualRows: 10})
	store.Record(Feedback{Template: "fp", ActualRows: 30})
	if len(invalidator.keys) != 1 || invalidator.keys[0] != "fp" {
		t.Fatalf("invalidations = %v", invalidator.keys)
	}
}

func TestAnalyzePolicyRequiresExplicitReadOnlyEnable(t *testing.T) {
	sampler := FakeAnalyzeSampler{Plan: Plan{EstimatedRows: 7}}
	policy := AnalyzePolicy{Sampler: sampler, Config: AnalyzeConfig{Enabled: true, ReadOnly: true, SampleRate: 1}}
	plan, sampled, err := policy.Sample(
		context.Background(),
		codegen.Compiled{SQL: "SELECT 1", ReadOnly: true},
	)
	if err != nil || !sampled || plan.EstimatedRows != 7 {
		t.Fatalf("plan=%+v sampled=%v err=%v", plan, sampled, err)
	}
	_, sampled, _ = policy.Sample(context.Background(), codegen.Compiled{SQL: "DELETE FROM t"})
	if sampled {
		t.Fatal("write statements must never be sampled")
	}
}

type analyzeSamplerFunc func(context.Context, codegen.Compiled) (Plan, error)

func (f analyzeSamplerFunc) ExplainAnalyze(ctx context.Context, c codegen.Compiled) (Plan, error) {
	return f(ctx, c)
}

func TestAnalyzePolicyUsesIndependentTimeout(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	policy := AnalyzePolicy{
		Sampler: analyzeSamplerFunc(func(ctx context.Context, c codegen.Compiled) (Plan, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 200*time.Millisecond {
				t.Fatalf("sampling deadline = %v, ok = %v", deadline, ok)
			}
			return Plan{ActualRows: 3}, nil
		}),
		Config: AnalyzeConfig{Enabled: true, ReadOnly: true, SampleRate: 1, Timeout: 100 * time.Millisecond},
	}
	plan, sampled, err := policy.Sample(parent, codegen.Compiled{SQL: "SELECT 1", ReadOnly: true})
	if err != nil || !sampled || plan.ActualRows != 3 {
		t.Fatalf("plan=%+v sampled=%v err=%v", plan, sampled, err)
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
