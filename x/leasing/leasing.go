// Package leasing runs scan batches across processes. A batch (kept in a
// catalog.BatchStore) is leased by one run of it at a time, which beats while
// it runs: it goes on when the console that started it is closed, every
// replica shows it, and when its owner stops beating, as on a restart,
// another process takes it over and does what is left. Every write of a run
// moves one item from the state it expects, on the condition that the run
// still owns the unfinished batch (catalog.BatchStore.MoveItem), so runs
// never overwrite each other. An item that fails is tried again after a
// backoff, a few times.
package leasing

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

// Options tune a Runner; a zero value takes its default.
type Options struct {
	// Parallel bounds the items of one batch run at once (2).
	Parallel int
	// Beat is how often a run records that it still runs a batch (5s); a
	// batch whose owner missed Stale (30s) is taken over.
	Beat, Stale time.Duration
	// Attempts bounds the runs of an item (3); Backoff is the wait before
	// the second (5s), doubling for each one after.
	Attempts int
	Backoff  time.Duration
	// MoveTries bounds the writes of one move when the store fails (4); the
	// first retry waits MoveBackoff (100ms), doubling after.
	MoveTries   int
	MoveBackoff time.Duration
	// Now is the clock (time.Now); Bus runs the goroutines (nil runs them
	// plain).
	Now func() time.Time
	Bus *bus.Bus
}

// withDefaults fills in what is not positive: no worker would never finish
// a batch, and a ticker refuses a beat that is not.
func (o Options) withDefaults() Options {
	o.Parallel = positive(o.Parallel, 2)
	o.Beat, o.Stale = positive(o.Beat, 5*time.Second), positive(o.Stale, 30*time.Second)
	o.Attempts, o.Backoff = positive(o.Attempts, 3), positive(o.Backoff, 5*time.Second)
	o.MoveTries, o.MoveBackoff = positive(o.MoveTries, 4), positive(o.MoveBackoff, 100*time.Millisecond)
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

func positive[T int | time.Duration](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}

// Work does one item of a batch of datasource, the schema, and says how it
// went: catalog.ItemDone, catalog.ItemFailed with why, or
// catalog.ItemCanceled; "" when ctx ended first (whoever runs the batch
// next, or its end, settles the item).
type Work func(ctx context.Context, datasource, schema string) (state, msg string)

// Resolver gives the Work of a datasource as configured now, for a batch
// taken over.
type Resolver func(ctx context.Context, datasource string) (Work, error)

// Runner runs the batches this process owns.
type Runner struct {
	store catalog.BatchStore
	opts  Options
	owner string
	mu    sync.Mutex
	// running are the batches this process runs, by ID, with how to stop
	// them.
	running map[string]context.CancelFunc
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
}

// New is a Runner of the batches in store.
func New(store catalog.BatchStore, opts Options) *Runner {
	ctx, stop := context.WithCancel(context.Background())
	return &Runner{
		store: store, opts: opts.withDefaults(), owner: randomID(), running: map[string]context.CancelFunc{},
		ctx: ctx, stop: stop,
	}
}

// Close stops the batches this process runs (they stay unfinished, for a
// takeover) and waits for them.
func (r *Runner) Close() {
	r.stop()
	r.wg.Wait()
}

// Owner names this process; a run of a batch is named Owner().<run>.
func (r *Runner) Owner() string { return r.owner }

// run names one run of a batch by this process: a takeover, even by this
// process again, is a new run.
func (r *Runner) run() string { return r.owner + "." + randomID() }

func randomID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// Start creates b, its ID, owner and times set here, and runs it; false
// with the batch started before by b's RequestKey instead, which is not
// run again. catalog.ErrTooManyBatches when b's datasource has maxOpen
// unfinished batches.
func (r *Runner) Start(ctx context.Context, b catalog.Batch, maxOpen int, work Work) (catalog.Batch, bool, error) {
	now := r.opts.Now()
	b.ID, b.Owner, b.StartedAt, b.Heartbeat = randomID(), r.run(), now, now
	got, created, err := r.store.CreateBatch(ctx, b, maxOpen)
	if err != nil {
		return catalog.Batch{}, false, err
	}
	if created {
		r.runBatch(got, work)
	}
	return got, created, nil
}

// Running reports whether this process runs the batch.
func (r *Runner) Running(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[id]
	return ok
}

// Stop stops a batch this process runs, at once, settling what was left.
func (r *Runner) Stop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stop, ok := r.running[id]; ok {
		stop()
	}
}

// batchRun is one run of a batch by this process. lost is set once the run
// stops without ending the batch: another run owns it (or it ended), or the
// store kept failing, and then whoever takes the batch over goes on with it.
type batchRun struct {
	id, datasource, owner string
	lost                  atomic.Bool
	stop                  context.CancelFunc
}

// runBatch runs the pending items of b in the background; b's owner is this
// run.
func (r *Runner) runBatch(b catalog.Batch, work Work) {
	ctx, cancel := context.WithCancel(r.ctx)
	r.mu.Lock()
	r.running[b.ID] = cancel
	r.mu.Unlock()
	br := &batchRun{id: b.ID, datasource: b.Datasource, owner: b.Owner, stop: cancel}
	var pending []catalog.BatchItem
	for _, it := range b.Items {
		if it.State == catalog.ItemPending {
			pending = append(pending, it)
		}
	}
	r.opts.Bus.Fork(&r.wg, "scan batch", func(busCtx context.Context) {
		// Closing the bus stops the batches as the process stopping does: they
		// stay unfinished, for another run to take over.
		defer context.AfterFunc(busCtx, r.stop)()
		defer cancel()
		defer func() {
			r.mu.Lock()
			delete(r.running, b.ID)
			r.mu.Unlock()
		}()
		r.execute(ctx, br, pending, work)
	})
}

// move moves an item from one of the states from; false when it was not in
// any of them, or when the run stops: it lost the batch, or the store failed
// MoveTries times in a row (the batch then stays unfinished, its items as
// last recorded, and is taken over once its heartbeat is stale).
//
// A write that failed may still have been made (its answer was lost): when a
// later try finds the item no longer in from, the item is read back, and the
// move counts as made when it is what was written.
func (r *Runner) move(br *batchRun, it catalog.BatchItem, from ...string) bool {
	wait := r.opts.MoveBackoff
	failed := false
	for try := 1; !br.lost.Load(); try++ {
		ctx, cancel := r.storeContext()
		moved, err := r.store.MoveItem(ctx, br.id, br.owner, r.opts.Now(), it, from...)
		cancel()
		switch {
		case err == nil && (moved || !failed):
			return moved
		case err == nil:
			made, readErr := r.written(br, it)
			if readErr == nil {
				return made
			}
			slog.Error("scan batch progress unknown: the run stops, the batch is taken over later",
				"batch", br.id, "schema", it.Schema, "error", readErr.Error())
		case errors.Is(err, catalog.ErrNotOwner), r.ctx.Err() != nil: // another run owns it, or this process stops
		case try < r.opts.MoveTries:
			slog.Warn("scan batch progress not recorded, trying again", "batch", br.id, "schema", it.Schema,
				"error", err.Error())
			failed = true
			sleep(r.ctx, wait)
			wait *= 2
			continue
		default:
			slog.Error("scan batch progress not recorded: the run stops, the batch is taken over later",
				"batch", br.id, "schema", it.Schema, "error", err.Error())
		}
		br.lost.Store(true)
		br.stop()
	}
	return false
}

// storeContext bounds a write of a run's progress: by the process stopping
// (not the batch: a batch stopped settles what was left), and by Stale, past
// which the run has lost the batch anyway.
func (r *Runner) storeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.ctx, r.opts.Stale)
}

// written reports whether the item of a batch is it, as a move wrote it.
func (r *Runner) written(br *batchRun, it catalog.BatchItem) (bool, error) {
	ctx, cancel := r.storeContext()
	defer cancel()
	b, ok, err := r.store.Batch(ctx, br.id)
	if err != nil || !ok {
		return false, cmp.Or(err, errors.New("the batch is gone"))
	}
	for _, got := range b.Items {
		if got.Schema == it.Schema {
			return got.State == it.State && got.Error == it.Error && got.Attempts == it.Attempts &&
				sameInstant(got.NotBefore, it.NotBefore), nil
		}
	}
	return false, nil
}

// sameInstant compares times as a store keeps them, to the microsecond: a
// time read back has lost what was finer.
func sameInstant(a, b time.Time) bool {
	return a.IsZero() == b.IsZero() && a.UnixMicro() == b.UnixMicro()
}

// execute runs the items, a few at a time, each when its NotBefore comes,
// and ends the batch, unless the process stops or the run lost the batch.
func (r *Runner) execute(ctx context.Context, br *batchRun, items []catalog.BatchItem, work Work) {
	r.opts.Bus.Go("scan batch heartbeat", func(context.Context) { r.beat(ctx, br) })
	queue := make(chan catalog.BatchItem, len(items))
	var left sync.WaitGroup
	left.Add(len(items))
	for _, it := range items {
		r.later(ctx, queue, it)
	}
	var wg sync.WaitGroup
	for range r.opts.Parallel {
		r.opts.Bus.Fork(&wg, "scan batch worker", func(context.Context) {
			for it := range queue {
				if ctx.Err() != nil || !r.runItem(ctx, br, it, work, queue) {
					left.Done()
				}
			}
		})
	}
	left.Wait()
	close(queue)
	wg.Wait()
	if r.ctx.Err() != nil || br.lost.Load() {
		return // this process stops, or another runs the batch now
	}
	finishing, cancel := r.storeContext()
	defer cancel()
	if err := r.store.FinishBatch(finishing, br.id, br.owner, r.opts.Now()); err != nil &&
		!errors.Is(err, catalog.ErrNotOwner) {
		slog.Warn("scan batch not finished", "batch", br.id, "error", err.Error())
	}
}

// later queues it for a worker when its NotBefore comes, or at once when ctx
// ends (the worker then lets it go).
func (r *Runner) later(ctx context.Context, queue chan<- catalog.BatchItem, it catalog.BatchItem) {
	wait := it.NotBefore.Sub(r.opts.Now()) // by the clock that set it
	if wait <= 0 {
		queue <- it
		return
	}
	r.opts.Bus.Fork(&r.wg, "scan batch retry", func(context.Context) {
		sleep(ctx, wait)
		queue <- it
	})
}

// beat records that this run still owns a batch, and stops it when it was
// canceled or another run took it over.
func (r *Runner) beat(ctx context.Context, br *batchRun) {
	t := time.NewTicker(r.opts.Beat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := r.opts.Now()
		owned, err := r.store.ClaimBatch(ctx, br.id, br.owner, now.Add(-r.opts.Stale), now)
		if err != nil {
			continue
		}
		if !owned {
			br.lost.Store(true)
			br.stop()
			return
		}
		if canceled, err := r.store.BatchCanceled(ctx, br.id); err == nil && canceled {
			br.stop()
			return
		}
	}
}

// runItem does one item and records how it went; true when it queued the
// item again to retry it.
func (r *Runner) runItem(
	ctx context.Context, br *batchRun, it catalog.BatchItem, work Work, queue chan<- catalog.BatchItem,
) bool {
	it.State, it.Attempts, it.Error, it.NotBefore = catalog.ItemRunning, it.Attempts+1, "", time.Time{}
	if !r.move(br, it, catalog.ItemPending) {
		return false
	}
	state, msg := work(ctx, br.datasource, it.Schema)
	switch state {
	case "":
		return false // stopped: whoever runs the batch next, or its end, settles the item
	case catalog.ItemFailed:
		if it.Attempts < r.opts.Attempts {
			it.State, it.Error = catalog.ItemPending, msg
			it.NotBefore = r.opts.Now().Add(r.opts.Backoff << (it.Attempts - 1))
			if r.move(br, it, catalog.ItemRunning) {
				r.later(ctx, queue, it)
				return true
			}
			return false
		}
	}
	it.State, it.Error = state, msg
	r.move(br, it, catalog.ItemRunning)
	return false
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// TakeOver runs the unfinished batches whose owner stopped beating: what
// they did not do yet is done here, and the items they were doing again.
// resolve says how to work a datasource as configured now.
func (r *Runner) TakeOver(ctx context.Context, resolve Resolver) {
	open, err := r.store.UnfinishedBatches(ctx)
	if err != nil {
		slog.Warn("scan batches not read", "error", err.Error())
		return
	}
	for _, b := range open {
		if r.Running(b.ID) {
			continue
		}
		now, owner := r.opts.Now(), r.run()
		if claimed, err := r.store.ClaimBatch(ctx, b.ID, owner, now.Add(-r.opts.Stale), now); err != nil || !claimed {
			continue
		}
		b.Owner, b.Heartbeat = owner, now
		if b.Canceled {
			// Canceled before its run ended: it ends now, not scanned again.
			r.finish(&b, "canceled")
			continue
		}
		work, err := resolve(ctx, b.Datasource)
		if errors.Is(err, ErrGone) {
			r.finish(&b, "datasource gone")
			continue
		}
		if err != nil {
			slog.Warn("scan batch not resumed", "batch", b.ID, "error", err.Error())
			continue
		}
		if !r.requeueRunning(&b) {
			// Left unfinished, as it was: taken over again once stale.
			slog.Warn("scan batch not resumed: its running schemas could not be queued again", "batch", b.ID)
			continue
		}
		slog.Info("scan batch taken over", "batch", b.ID, "datasource", b.Datasource)
		r.runBatch(b, work)
	}
}

// ErrGone is the error of a Resolver for a datasource no longer configured:
// its batches end (their items left canceled) instead of being taken over
// again and again, counting against the open batches for good.
var ErrGone = errors.New("leasing: the datasource is no longer configured")

// finish ends a batch this process claimed without running it, its items
// left canceled.
func (r *Runner) finish(b *catalog.Batch, why string) {
	ctx, cancel := r.storeContext()
	defer cancel()
	if err := r.store.FinishBatch(ctx, b.ID, b.Owner, r.opts.Now()); err != nil {
		slog.Warn("scan batch not ended", "batch", b.ID, "why", why, "error", err.Error())
		return
	}
	slog.Info("scan batch ended without running", "batch", b.ID, "why", why)
}

// requeueRunning moves the items the last owner was doing back to pending,
// as attempts that did not end; false when one of them could not be, and
// the batch is not to be run.
func (r *Runner) requeueRunning(b *catalog.Batch) bool {
	br := &batchRun{id: b.ID, owner: b.Owner, stop: func() {}}
	for i, it := range b.Items {
		if it.State != catalog.ItemRunning {
			continue
		}
		it.State = catalog.ItemPending
		if !r.move(br, it, catalog.ItemRunning) {
			return false
		}
		b.Items[i] = it
	}
	return true
}

// Watch takes over the batches other processes stopped running, now and
// every Stale, until Close.
func (r *Runner) Watch(resolve Resolver) {
	r.opts.Bus.Fork(&r.wg, "scan batch takeover", func(busCtx context.Context) {
		defer context.AfterFunc(busCtx, r.stop)() // closing the bus stops the batches
		for {
			r.TakeOver(r.ctx, resolve)
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(r.opts.Stale):
			}
		}
	})
}
