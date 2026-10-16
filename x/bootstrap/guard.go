package bootstrap

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

// health is the circuit breaker and adaptive concurrency limit of one
// database connection: a database failing or slowing down is held back on
// its own, and the others serve on. It follows the connection across
// reloads (see Shared); either part is nil when rate limiting is off.
type health struct {
	breaker atomic.Pointer[ratelimit.Breaker]
	limiter atomic.Pointer[ratelimit.Adaptive]
}

// set replaces the breaker and the limiter (new settings start afresh).
func (h *health) set(breaker *ratelimit.Breaker, limiter *ratelimit.Adaptive) {
	h.breaker.Store(breaker)
	h.limiter.Store(limiter)
}

// enter admits a call on the connection; done ends it with its error, and
// with how long it took for the limiter (a query holds its place until its
// rows close).
func (h *health) enter() (done func(error), err error) {
	breaker, limiter := h.breaker.Load(), h.limiter.Load()
	var probe uint64
	if breaker != nil {
		if probe, err = breaker.Allow(); err != nil {
			return nil, err
		}
	}
	if limiter != nil {
		if err := limiter.Acquire(); err != nil {
			if breaker != nil {
				breaker.Release(probe)
			}
			return nil, err
		}
	}
	start := time.Now()
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			counted := countsAgainstHealth(err)
			if limiter != nil {
				if counted {
					limiter.OnResult(err, time.Since(start))
				}
				limiter.Release()
			}
			switch {
			case breaker == nil:
			case counted:
				breaker.Record(err == nil)
			default:
				breaker.Release(probe)
			}
		})
	}, nil
}

// countsAgainstHealth reports whether a call's outcome tells of the
// database's health: success does, and a failure the database or the
// connection reported, but not one the caller caused (a cancelled call, a
// statement the database refused for its data, its privileges, or a schema
// that changed under the configuration).
func countsAgainstHealth(err error) bool {
	var constraint *store.ConstraintError
	switch {
	case err == nil:
		return true
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &constraint), errors.Is(err, store.ErrPermissionDenied),
		errors.Is(err, store.ErrMissingRelation), errors.Is(err, store.ErrMissingColumn),
		errors.Is(err, store.ErrStalePlan):
		return false
	}
	return true
}

// guardedDB runs a connection's queries and writes under its health.
type guardedDB struct {
	store.DB
	health *health
}

func (d guardedDB) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	done, err := d.health.enter()
	if err != nil {
		return nil, err
	}
	rows, err := d.DB.QueryContext(ctx, query, args...)
	if err != nil {
		done(err)
		return nil, err
	}
	return &guardedRows{Rows: rows, done: done}, nil
}

func (d guardedDB) ExecContext(ctx context.Context, query string, args ...any) (store.Result, error) {
	done, err := d.health.enter()
	if err != nil {
		return store.Result{}, err
	}
	res, err := d.DB.ExecContext(ctx, query, args...)
	done(err)
	return res, err
}

// guardedRows ends its query when closed, with the error iterating met.
type guardedRows struct {
	store.Rows
	done func(error)
}

func (r *guardedRows) Close() error {
	err := r.Rows.Close()
	iterErr := r.Err()
	if iterErr == nil {
		iterErr = err
	}
	r.done(iterErr)
	return err
}

// guardedExplainer plans a connection's statements under its health: a
// database its breaker holds back is not asked for EXPLAIN either, which
// would wait on it for the query timeout and hold an IO slot meanwhile.
type guardedExplainer struct {
	cost.Explainer
	health *health
}

func (e guardedExplainer) Explain(ctx context.Context, query string, args []any) (cost.Plan, error) {
	done, err := e.health.enter()
	if err != nil {
		return cost.Plan{}, err
	}
	plan, err := e.Explainer.Explain(ctx, query, args)
	done(err)
	return plan, err
}

// guardedSampler samples a connection's plans under its health (see
// guardedExplainer).
type guardedSampler struct {
	cost.AnalyzeSampler
	health *health
}

func (s guardedSampler) ExplainAnalyze(ctx context.Context, compiled codegen.Compiled) (cost.Plan, error) {
	done, err := s.health.enter()
	if err != nil {
		return cost.Plan{}, err
	}
	plan, err := s.AnalyzeSampler.ExplainAnalyze(ctx, compiled)
	done(err)
	return plan, err
}

// guardedBeginner begins a connection's transactions under its health. One
// per connection, shared by the Apps of the process: a transaction's
// connection is compared by identity (see tool.TransactionManager).
type guardedBeginner struct {
	store.TxBeginner
	health *health
}

func (b *guardedBeginner) BeginTx(ctx context.Context, opts *store.TxOptions) (store.Tx, error) {
	done, err := b.health.enter()
	if err != nil {
		return nil, err
	}
	tx, err := b.TxBeginner.BeginTx(ctx, opts)
	done(err)
	return tx, err
}
