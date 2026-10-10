package configapp

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

// Scan batches scan many schemas of a datasource on the server, a few at a
// time through the scan pool. A batch is kept in the catalog store, each of
// its schemas with its state: it goes on when the console that started it is
// closed, every replica shows it, and when its owner (one run of it by a
// process) stops beating, as on a restart, another process takes it over and
// scans what is left. Every write of the owner moves one schema from the
// state it expects, on the condition that it still owns the unfinished batch
// (catalog.BatchStore.MoveItem), so runs never overwrite each other. A scan
// that fails is tried again after a backoff, a few times.

const (
	batchParallel = 2
	batchPoll     = 200 * time.Millisecond
	// batchBeat is how often an owner records that it runs a batch; a
	// batch whose owner missed batchStale is taken over.
	batchBeat  = 5 * time.Second
	batchStale = 30 * time.Second
	// batchList bounds the batches listed per datasource.
	batchList = 10
	// batchOpen bounds the unfinished batches of a datasource and
	// batchSchemas the schemas of one, so requests cannot pile up work.
	batchOpen    = 4
	batchSchemas = 5000
	// batchAttempts bounds the scans of a schema; batchBackoff is the wait
	// before the second, doubling for each one after.
	batchAttempts = 3
	batchBackoff  = 5 * time.Second
	// moveTries bounds the writes of one move when the store fails; the
	// first retry waits moveBackoff, doubling after.
	moveTries   = 4
	moveBackoff = 100 * time.Millisecond
)

// scanRunner scans one schema of a datasource.
type scanRunner func(schema string) func(context.Context) (struct{}, error)

// batches runs the scan batches this process owns.
type batches struct {
	owner string
	mu    sync.Mutex
	// running are the batches this process runs, by ID, with how to stop
	// them.
	running map[string]context.CancelFunc
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	// backoff is the wait before the second scan of a schema.
	backoff time.Duration
}

func newBatches() *batches {
	ctx, stop := context.WithCancel(context.Background())
	return &batches{owner: randomID(), running: map[string]context.CancelFunc{}, ctx: ctx, stop: stop,
		backoff: batchBackoff}
}

func (b *batches) close() {
	b.stop()
	b.wg.Wait()
}

// run names one run of a batch by this process: a takeover, even by this
// process again, is a new run.
func (b *batches) run() string { return b.owner + "." + randomID() }

func randomID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// startBatch records a batch of schemas and runs it; a request sent again
// with its key gets the batch it started.
func (s *Scans) startBatch(
	ctx context.Context, datasource, who, key string, schemas []string, run scanRunner,
) (*ScanBatch, error) {
	now := s.now()
	b := catalog.Batch{ID: randomID(), Datasource: datasource, StartedBy: who, RequestKey: key,
		Owner: s.batches.run(), StartedAt: now, Heartbeat: now, Items: make([]catalog.BatchItem, 0, len(schemas))}
	for _, schema := range schemas {
		b.Items = append(b.Items, catalog.BatchItem{Schema: schema, State: catalog.ItemPending})
	}
	got, created, err := s.store.CreateBatch(ctx, b, batchOpen)
	if errors.Is(err, catalog.ErrTooManyBatches) {
		return nil, fmt.Errorf("%s has %d scan batches running: wait for them or stop one", datasource, batchOpen)
	}
	if err != nil {
		return nil, err
	}
	if created {
		s.runBatch(got, run)
	}
	return viewBatch(got), nil
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
func (s *Scans) runBatch(b catalog.Batch, run scanRunner) {
	ctx, cancel := context.WithCancel(s.batches.ctx)
	s.batches.mu.Lock()
	s.batches.running[b.ID] = cancel
	s.batches.mu.Unlock()
	br := &batchRun{id: b.ID, datasource: b.Datasource, owner: b.Owner, stop: cancel}
	var pending []catalog.BatchItem
	for _, it := range b.Items {
		if it.State == catalog.ItemPending {
			pending = append(pending, it)
		}
	}
	s.bus.Fork(&s.batches.wg, "scan batch", func(busCtx context.Context) {
		// Closing the bus stops the batches as the process stopping does: they
		// stay unfinished, for another run to take over.
		defer context.AfterFunc(busCtx, s.batches.stop)()
		defer cancel()
		defer func() {
			s.batches.mu.Lock()
			delete(s.batches.running, b.ID)
			s.batches.mu.Unlock()
		}()
		s.execute(ctx, br, pending, run)
	})
}

// move moves an item from one of the states from; false when it was not in
// any of them, or when the run stops: it lost the batch, or the store failed
// moveTries times in a row (the batch then stays unfinished, its items as
// last recorded, and is taken over once its heartbeat is stale).
//
// A write that failed may still have been made (its answer was lost): when a
// later try finds the item no longer in from, the item is read back, and the
// move counts as made when it is what was written.
func (s *Scans) move(br *batchRun, it catalog.BatchItem, from ...string) bool {
	wait := moveBackoff
	failed := false
	for try := 1; !br.lost.Load(); try++ {
		moved, err := s.store.MoveItem(context.Background(), br.id, br.owner, s.now(), it, from...)
		switch {
		case err == nil && (moved || !failed):
			return moved
		case err == nil:
			made, readErr := s.written(br, it)
			if readErr == nil {
				return made
			}
			slog.Error("scan batch progress unknown: the run stops, the batch is taken over later",
				"batch", br.id, "schema", it.Schema, "error", readErr.Error())
		case errors.Is(err, catalog.ErrNotOwner):
		case try < moveTries:
			slog.Warn("scan batch progress not recorded, trying again", "batch", br.id, "schema", it.Schema,
				"error", err.Error())
			failed = true
			time.Sleep(wait)
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

// written reports whether the item of a batch is it, as a move wrote it.
func (s *Scans) written(br *batchRun, it catalog.BatchItem) (bool, error) {
	b, ok, err := s.store.Batch(context.Background(), br.id)
	if err != nil || !ok {
		return false, cmp.Or(err, errors.New("the batch is gone"))
	}
	for _, got := range b.Items {
		if got.Schema == it.Schema {
			return got.State == it.State && got.Error == it.Error && got.Attempts == it.Attempts &&
				got.NotBefore.Equal(it.NotBefore), nil
		}
	}
	return false, nil
}

// execute scans the items, a few at a time, each when its NotBefore comes,
// and ends the batch, unless the process stops or the run lost the batch.
func (s *Scans) execute(ctx context.Context, br *batchRun, items []catalog.BatchItem, run scanRunner) {
	s.bus.Go("scan batch heartbeat", func(context.Context) { s.beat(ctx, br) })
	work := make(chan catalog.BatchItem, len(items))
	var left sync.WaitGroup
	left.Add(len(items))
	for _, it := range items {
		s.later(ctx, work, it)
	}
	var wg sync.WaitGroup
	for range batchParallel {
		s.bus.Fork(&wg, "scan batch worker", func(context.Context) {
			for it := range work {
				if ctx.Err() != nil || !s.runItem(ctx, br, it, run, work) {
					left.Done()
				}
			}
		})
	}
	left.Wait()
	close(work)
	wg.Wait()
	if s.batches.ctx.Err() != nil || br.lost.Load() {
		return // this process stops, or another runs the batch now
	}
	if err := s.store.FinishBatch(context.Background(), br.id, br.owner, s.now()); err != nil &&
		!errors.Is(err, catalog.ErrNotOwner) {
		slog.Warn("scan batch not finished", "batch", br.id, "error", err.Error())
	}
}

// later queues it for a worker when its NotBefore comes, or at once when ctx
// ends (the worker then lets it go).
func (s *Scans) later(ctx context.Context, work chan<- catalog.BatchItem, it catalog.BatchItem) {
	wait := time.Until(it.NotBefore)
	if wait <= 0 {
		work <- it
		return
	}
	s.bus.Fork(&s.batches.wg, "scan batch retry", func(context.Context) {
		sleep(ctx, wait)
		work <- it
	})
}

// beat records that this run still owns a batch, and stops it when it was
// canceled or another run took it over.
func (s *Scans) beat(ctx context.Context, br *batchRun) {
	t := time.NewTicker(batchBeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := s.now()
		owned, err := s.store.ClaimBatch(ctx, br.id, br.owner, now.Add(-batchStale), now)
		if err != nil {
			continue
		}
		if !owned {
			br.lost.Store(true)
			br.stop()
			return
		}
		if b, _, err := s.store.Batch(ctx, br.id); err == nil && b.Canceled {
			br.stop()
			return
		}
	}
}

// runItem scans one schema through the pool, waiting for a free slot, and
// records how it went; true when it queued the item again to retry it.
func (s *Scans) runItem(
	ctx context.Context, br *batchRun, it catalog.BatchItem, run scanRunner, work chan<- catalog.BatchItem,
) bool {
	it.State, it.Attempts, it.Error, it.NotBefore = catalog.ItemRunning, it.Attempts+1, "", time.Time{}
	if !s.move(br, it, catalog.ItemPending) {
		return false
	}
	state, msg := s.scan(ctx, br.datasource, run(it.Schema))
	switch state {
	case "":
		return false // stopped: whoever runs the batch next, or its end, settles the item
	case catalog.ItemFailed:
		if it.Attempts < batchAttempts {
			it.State, it.Error = catalog.ItemPending, msg
			it.NotBefore = s.now().Add(s.batches.backoff << (it.Attempts - 1))
			if s.move(br, it, catalog.ItemRunning) {
				s.later(ctx, work, it)
				return true
			}
			return false
		}
	}
	it.State, it.Error = state, msg
	s.move(br, it, catalog.ItemRunning)
	return false
}

// scan runs one scan through the pool and says how it ended: done, failed
// (with why) or canceled; "" when ctx ended first.
func (s *Scans) scan(
	ctx context.Context, datasource string, run func(context.Context) (struct{}, error),
) (string, string) {
	var job scanjobs.Job[struct{}]
	for {
		if ctx.Err() != nil {
			return "", ""
		}
		var err error
		if job, err = s.pool.Start(datasource, run); err == nil {
			break
		} else if !errors.Is(err, scanjobs.ErrBusy) {
			return catalog.ItemFailed, err.Error()
		}
		sleep(ctx, time.Second)
	}
	for {
		sleep(ctx, batchPoll)
		if ctx.Err() != nil {
			s.pool.Cancel(job.ID)
			return "", ""
		}
		current, ok := s.pool.Get(job.ID)
		switch {
		case !ok:
			return catalog.ItemFailed, "the scan expired"
		case current.State == scanjobs.Done:
			return catalog.ItemDone, ""
		case current.State == scanjobs.Failed:
			return catalog.ItemFailed, current.Err
		case current.State == scanjobs.Canceled:
			return catalog.ItemCanceled, ""
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// takeOver runs the unfinished batches whose owner stopped beating: what
// they did not scan yet is scanned here, and the schemas they were scanning
// again. runner says how to scan a datasource as configured now.
func (s *Scans) takeOver(ctx context.Context, runner func(ctx context.Context, datasource string) (scanRunner, error)) {
	open, err := s.store.UnfinishedBatches(ctx)
	if err != nil {
		slog.Warn("scan batches not read", "error", err.Error())
		return
	}
	for _, b := range open {
		s.batches.mu.Lock()
		_, mine := s.batches.running[b.ID]
		s.batches.mu.Unlock()
		if mine {
			continue
		}
		now, owner := s.now(), s.batches.run()
		if claimed, err := s.store.ClaimBatch(ctx, b.ID, owner, now.Add(-batchStale), now); err != nil || !claimed {
			continue
		}
		run, err := runner(ctx, b.Datasource)
		if err != nil {
			slog.Warn("scan batch not resumed", "batch", b.ID, "error", err.Error())
			continue
		}
		b.Owner, b.Heartbeat = owner, now
		if !s.requeueRunning(&b) {
			// Left unfinished, as it was: taken over again once stale.
			slog.Warn("scan batch not resumed: its running schemas could not be queued again", "batch", b.ID)
			continue
		}
		slog.Info("scan batch taken over", "batch", b.ID, "datasource", b.Datasource)
		s.runBatch(b, run)
	}
}

// requeueRunning moves the items the last owner was scanning back to
// pending, as attempts that did not end; false when one of them could not
// be, and the batch is not to be run.
func (s *Scans) requeueRunning(b *catalog.Batch) bool {
	br := &batchRun{id: b.ID, owner: b.Owner, stop: func() {}}
	for i, it := range b.Items {
		if it.State != catalog.ItemRunning {
			continue
		}
		it.State = catalog.ItemPending
		if !s.move(br, it, catalog.ItemRunning) {
			return false
		}
		b.Items[i] = it
	}
	return true
}

// WatchBatches takes over the batches other processes stopped running, now
// and every batchStale, until the scans close.
func (s *Scans) WatchBatches(runner func(ctx context.Context, datasource string) (scanRunner, error)) {
	s.bus.Fork(&s.batches.wg, "scan batch takeover", func(busCtx context.Context) {
		defer context.AfterFunc(busCtx, s.batches.stop)() // closing the bus stops the batches
		for {
			s.takeOver(s.batches.ctx, runner)
			select {
			case <-s.batches.ctx.Done():
				return
			case <-time.After(batchStale):
			}
		}
	})
}

func viewBatch(b catalog.Batch) *ScanBatch {
	out := &ScanBatch{
		ID: b.ID, Datasource: b.Datasource, State: "running", Total: len(b.Items), StartedBy: b.StartedBy,
		StartedAt: b.StartedAt, Items: make([]ScanBatchItem, 0, len(b.Items)),
	}
	if !b.FinishedAt.IsZero() {
		out.State, out.FinishedAt = "done", &b.FinishedAt
	}
	for _, item := range b.Items {
		switch item.State {
		case "done":
			out.Done++
		case "failed":
			out.Failed++
		case "canceled":
			out.Canceled++
		default:
			out.Pending++
		}
		out.Items = append(
			out.Items,
			ScanBatchItem{Schema: item.Schema, State: item.State, Error: optional(item.Error)},
		)
	}
	if out.State == "done" && out.Canceled > 0 {
		out.State = "canceled"
	}
	return out
}

// runnerFor scans datasource as the published configuration has it now.
func (r *Service) runnerFor(ctx context.Context, datasource string) (scanRunner, error) {
	scans, err := r.scans()
	if err != nil {
		return nil, err
	}
	cfg, err := r.SelectConfig(ctx, nil, nil)
	if err != nil {
		return nil, err
	}
	if !hasDatasource(cfg, datasource) {
		return nil, fmt.Errorf("unknown datasource %q", datasource)
	}
	src := sourceOf(cfg, datasource)
	return func(schema string) func(context.Context) (struct{}, error) {
		return r.scanRun(cfg, scans, src, datasource, []string{schema})
	}, nil
}

// WatchScanBatches takes over the scan batches of processes that stopped.
func (r *Service) WatchScanBatches() {
	if r.Scans != nil {
		r.Scans.WatchBatches(r.runnerFor)
	}
}

// StartScanBatch scans schemas of a datasource on the server, a few at a
// time; the same request sent again (by its key) gets the batch it started.
func (r *Service) StartScanBatch(ctx context.Context, in ScanBatchInput) (*ScanBatch, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	datasource, key := in.Datasource, strings.TrimSpace(deref(in.RequestKey))
	if len(key) > 64 {
		return nil, errors.New("a request key has at most 64 bytes")
	}
	schemas := slices.Compact(slices.Sorted(slices.Values(in.Schemas)))
	if len(schemas) == 0 {
		return nil, errors.New("name the schemas to scan")
	}
	if len(schemas) > batchSchemas {
		return nil, fmt.Errorf("a batch scans at most %d schemas", batchSchemas)
	}
	_, scans, _, err := r.scanContext(ctx, datasource)
	if err != nil {
		return nil, err
	}
	run, err := r.runnerFor(ctx, datasource)
	if err != nil {
		return nil, err
	}
	return scans.startBatch(ctx, datasource, who.Username, key, schemas, run)
}

// ScanBatches lists the recent batches of datasource, running ones first.
func (r *Service) ScanBatches(ctx context.Context, datasource string) ([]ScanBatch, error) {
	if _, err := auth.RequireDatasource(ctx, auth.PermWrite, datasource); err != nil {
		return nil, err
	}
	scans, err := r.scans()
	if err != nil {
		return nil, err
	}
	all, err := scans.store.Batches(ctx, datasource, batchList)
	if err != nil {
		return nil, err
	}
	out := make([]ScanBatch, 0, len(all))
	for _, b := range all {
		out = append(out, *viewBatch(b))
	}
	slices.SortStableFunc(
		out,
		func(a, b ScanBatch) int { return boolRank(a.State != "running") - boolRank(b.State != "running") },
	)
	return out, nil
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CancelScanBatch asks the owner of a batch to stop; this process stops at
// once a batch it runs.
func (r *Service) CancelScanBatch(ctx context.Context, id string) (*ScanBatch, error) {
	scans, err := r.batchOf(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := scans.store.CancelBatch(ctx, id); err != nil {
		return nil, err
	}
	scans.batches.mu.Lock()
	if stop, ok := scans.batches.running[id]; ok {
		stop()
	}
	scans.batches.mu.Unlock()
	b, ok, err := scans.store.Batch(ctx, id)
	if err != nil || !ok {
		return nil, err
	}
	return viewBatch(b), nil
}

// batchOf checks the caller may act on the datasource of a batch.
func (r *Service) batchOf(ctx context.Context, id string) (*Scans, error) {
	if _, err := auth.Require(ctx, auth.PermWrite); err != nil {
		return nil, err
	}
	scans, err := r.scans()
	if err != nil {
		return nil, err
	}
	b, ok, err := scans.store.Batch(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("unknown scan batch %q", id)
	}
	_, err = auth.RequireDatasource(ctx, auth.PermWrite, b.Datasource)
	return scans, err
}

// RetryScanBatch scans again, as a new batch, what failed or was canceled
// in a batch.
func (r *Service) RetryScanBatch(ctx context.Context, id string) (*ScanBatch, error) {
	scans, err := r.batchOf(ctx, id)
	if err != nil {
		return nil, err
	}
	b, _, err := scans.store.Batch(ctx, id)
	if err != nil {
		return nil, err
	}
	var again []string
	for _, item := range b.Items {
		if item.State == "failed" || item.State == "canceled" {
			again = append(again, item.Schema)
		}
	}
	if len(again) == 0 {
		return nil, errors.New("nothing in the batch failed or was canceled")
	}
	return r.StartScanBatch(ctx, ScanBatchInput{Datasource: b.Datasource, Schemas: again})
}
