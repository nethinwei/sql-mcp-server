package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/store"
	"github.com/nethinwei/sql-mcp-server/core/store/storetest"
)

// guardedPair is shared services with two connections, a and b, whose
// queries fail while their flag is set, under rate limits breaking after one
// failure.
func guardedPair(t *testing.T, failA, failB *bool) (*Shared, store.DB, store.DB, *config.Config) {
	t.Helper()
	s := NewShared()
	cfg := &config.Config{Databases: map[string]config.DatabaseConfig{"x": {Driver: "postgres", DSN: "x"}}}
	cfg.ApplyDefaults()
	cfg.RateLimit.BreakerThreshold, cfg.RateLimit.BreakerCooldown = 1, 50*time.Millisecond
	db := func(fail *bool) *storetest.FakeDB {
		return &storetest.FakeDB{QueryFn: func(context.Context, string, ...any) (store.Rows, error) {
			if *fail {
				return nil, errors.New("connection refused")
			}
			return storetest.NewFakeRows([]string{"id"}, []any{int64(1)}), nil
		}}
	}
	pa, pb := &fakeProvider{}, &fakeProvider{}
	gs := guards{"x": {}}
	for key, p := range map[string]Provider{"a": pa, "b": pb} {
		c := &sharedConnection{provider: p, health: &health{}, refs: 1}
		c.beginner = &guardedBeginner{TxBeginner: p, health: c.health}
		s.pools[key], gs["x"][key] = c, c
	}
	s.apply(&App{config: cfg})
	wrapA, _ := gs.guard("x", "a", pa)
	wrapB, _ := gs.guard("x", "b", pb)
	return s, wrapA(db(failA)), wrapB(db(failB)), cfg
}

func query(db store.DB) error {
	rows, err := db.QueryContext(context.Background(), "select 1")
	if err != nil {
		return err
	}
	return rows.Close()
}

// One database failing opens its connection's breaker only; the other serves
// on. Past the cooldown one probe goes through, and a reload with the same
// rate limits keeps the state.
func TestConnectionsHaveTheirOwnHealth(t *testing.T) {
	failA, failB := true, false
	s, a, b, cfg := guardedPair(t, &failA, &failB)
	if err := query(a); err == nil {
		t.Fatal("a failing query passed")
	}
	if err := query(a); !errors.Is(err, ratelimit.ErrCircuitOpen) {
		t.Fatalf("a's breaker did not open: %v", err)
	}
	if err := query(b); err != nil {
		t.Fatalf("b was held back by a: %v", err)
	}
	s.apply(&App{config: cfg}) // a reload, the same rate limits
	if err := query(a); !errors.Is(err, ratelimit.ErrCircuitOpen) {
		t.Fatalf("a reload reset a's breaker: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	failA = false
	if err := query(a); err != nil {
		t.Fatalf("the probe was refused: %v", err)
	}
	if err := query(a); err != nil {
		t.Fatalf("a good probe did not close the breaker: %v", err)
	}
}

// A call the caller cancelled says nothing of the database's health.
func TestCancelledCallsDoNotCount(t *testing.T) {
	if countsAgainstHealth(context.Canceled) || countsAgainstHealth(&store.ConstraintError{Kind: "unique"}) ||
		countsAgainstHealth(store.ErrPermissionDenied) || !countsAgainstHealth(errors.New("connection refused")) ||
		!countsAgainstHealth(nil) {
		t.Fatal("classification")
	}
	// Nor does a schema that changed under the configuration: a table dropped
	// must not take its datasource's other tables down with it.
	for _, changed := range []error{store.ErrMissingRelation, store.ErrMissingColumn, store.ErrStalePlan} {
		if countsAgainstHealth(fmt.Errorf("%w: gone", changed)) {
			t.Fatalf("%v counts against health", changed)
		}
	}
	// Nor does a value the caller gave that its column cannot hold: any
	// caller could otherwise open the breaker for everyone.
	if countsAgainstHealth(fmt.Errorf("%w: 22P02", store.ErrInvalidValue)) {
		t.Fatal("an invalid value counts against health")
	}
	// A statement the database stopped at its timeout counts: when the
	// database slows down, timeouts are how it shows.
	if !countsAgainstHealth(fmt.Errorf("%w: canceling statement", store.ErrStatementTimeout)) {
		t.Fatal("a database statement timeout must count against health")
	}
}

// The bound on results held in memory and the lifetimes of pooled
// connections say nothing of a connection's health: changing them keeps
// every breaker's state.
func TestResultMemoryAndPoolChangesKeepHealth(t *testing.T) {
	for name, change := range map[string]func(*config.RateLimitConfig){
		"result memory":       func(r *config.RateLimitConfig) { r.MaxResultMemory *= 2 },
		"connection idle":     func(r *config.RateLimitConfig) { r.ConnMaxIdleTime += time.Minute },
		"connection lifetime": func(r *config.RateLimitConfig) { r.ConnMaxLifetime += time.Minute },
	} {
		failA, failB := true, false
		s, a, _, cfg := guardedPair(t, &failA, &failB)
		if err := query(a); err == nil {
			t.Fatal("a failing query passed")
		}
		next := *cfg
		change(&next.RateLimit)
		s.apply(&App{config: &next})
		if err := query(a); !errors.Is(err, ratelimit.ErrCircuitOpen) {
			t.Fatalf("changing the %s reset a's breaker: %v", name, err)
		}
	}
}

// explainCount counts the EXPLAINs that reach the database.
type explainCount struct{ asked int }

func (e *explainCount) Explain(context.Context, string, []any) (cost.Plan, error) {
	e.asked++
	return cost.Plan{ScanType: cost.ScanIndex, StatsFresh: true}, nil
}

// A connection the breaker holds back is not asked to plan either: the cost
// gate's EXPLAIN reaches the database as its queries do, and the call is
// refused as they are.
func TestExplainIsHeldBackWithTheConnection(t *testing.T) {
	failA, failB := true, false
	s, a, _, _ := guardedPair(t, &failA, &failB)
	if err := query(a); err == nil {
		t.Fatal("a failing query passed")
	}
	count := &explainCount{}
	est := cost.Estimate{Explainer: guardedExplainer{Explainer: count, health: s.pools["a"].health}}
	_, err := est.Check(context.Background(), codegen.Compiled{SQL: "SELECT * FROM t"})
	if !errors.Is(err, ratelimit.ErrCircuitOpen) || count.asked != 0 {
		t.Fatalf("EXPLAIN reached the held-back database: err=%v asked=%d", err, count.asked)
	}
}
