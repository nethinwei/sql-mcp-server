package graph

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

// Scan batches scan many schemas of a datasource on the server, a few at a
// time through the scan pool. A batch is kept in the catalog store with its
// progress: it goes on when the console that started it is closed, every
// replica shows it, and when its owner (the process running it) stops
// beating, as on a restart, another process takes it over and scans what is
// left.

const (
	batchParallel = 2
	batchPoll     = 200 * time.Millisecond
	// batchBeat is how often an owner records that it runs a batch; a
	// batch whose owner missed batchStale is taken over.
	batchBeat  = 5 * time.Second
	batchStale = 30 * time.Second
	// batchList bounds the batches listed per datasource.
	batchList = 10
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
}

func newBatches() *batches {
	ctx, stop := context.WithCancel(context.Background())
	return &batches{owner: randomID(), running: map[string]context.CancelFunc{}, ctx: ctx, stop: stop}
}

func (b *batches) close() {
	b.stop()
	b.wg.Wait()
}

func randomID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// startBatch records a batch of schemas and runs it.
func (s *Scans) startBatch(
	ctx context.Context,
	datasource, who string,
	schemas []string,
	run scanRunner,
) (*ScanBatch, error) {
	now := s.now()
	b := catalog.Batch{ID: randomID(), Datasource: datasource, StartedBy: who, Owner: s.batches.owner, StartedAt: now,
		Heartbeat: now}
	for _, schema := range schemas {
		b.Items = append(b.Items, catalog.BatchItem{Schema: schema, State: "pending"})
	}
	if err := s.store.PutBatch(ctx, b); err != nil {
		return nil, err
	}
	view := viewBatch(b)
	s.runBatch(b, run)
	return view, nil
}

// runBatch runs b in the background; b's owner is this process.
func (s *Scans) runBatch(b catalog.Batch, run scanRunner) {
	ctx, cancel := context.WithCancel(s.batches.ctx)
	s.batches.mu.Lock()
	s.batches.running[b.ID] = cancel
	s.batches.mu.Unlock()
	s.batches.wg.Add(1)
	go func() {
		defer s.batches.wg.Done()
		defer cancel()
		defer func() {
			s.batches.mu.Lock()
			delete(s.batches.running, b.ID)
			s.batches.mu.Unlock()
		}()
		b.Items = slices.Clone(b.Items) // the run's own, guarded by its lock
		s.execute(ctx, cancel, &batchRun{batch: b}, run)
	}()
}

// batchRun is a batch as its owner runs it; the lock guards its items.
// lost is set once another process owns the batch: this one then writes
// nothing more.
type batchRun struct {
	mu    sync.Mutex
	batch catalog.Batch
	lost  bool
}

// save records the progress of a batch while this process still owns it.
func (s *Scans) save(br *batchRun) {
	now := s.now()
	owned, err := s.store.ClaimBatch(context.Background(), br.batch.ID, s.batches.owner, now.Add(-batchStale), now)
	br.mu.Lock()
	if err == nil && !owned {
		br.lost = true
	}
	b := br.batch
	b.Items, b.Heartbeat = slices.Clone(b.Items), now
	lost := br.lost
	br.mu.Unlock()
	if lost {
		return
	}
	if err := s.store.PutBatch(context.Background(), b); err != nil {
		slog.Warn("scan batch progress not recorded", "batch", b.ID, "error", err.Error())
	}
}

func (s *Scans) execute(ctx context.Context, cancel context.CancelFunc, br *batchRun, run scanRunner) {
	var pending []int
	for i, item := range br.batch.Items {
		if item.State == "pending" {
			pending = append(pending, i)
		}
	}
	go s.beat(ctx, cancel, br)
	var wg sync.WaitGroup
	next := make(chan int)
	for range batchParallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				br.mu.Lock()
				schema := br.batch.Items[i].Schema
				br.mu.Unlock()
				s.runItem(ctx, br, i, run(schema))
			}
		}()
	}
	for _, i := range pending {
		select {
		case next <- i:
		case <-ctx.Done():
		}
	}
	close(next)
	wg.Wait()
	br.mu.Lock()
	if s.batches.ctx.Err() != nil || br.lost {
		br.mu.Unlock()
		return // this process stops, or another runs the batch now
	}
	for i := range br.batch.Items {
		if st := br.batch.Items[i].State; st == "pending" || st == "running" {
			br.batch.Items[i].State = "canceled"
		}
	}
	br.batch.FinishedAt = s.now()
	br.mu.Unlock()
	s.save(br)
}

// beat records that this process still runs a batch, and stops it when it
// was canceled or another process took it over.
func (s *Scans) beat(ctx context.Context, cancel context.CancelFunc, br *batchRun) {
	t := time.NewTicker(batchBeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := s.now()
		owned, err := s.store.ClaimBatch(ctx, br.batch.ID, s.batches.owner, now.Add(-batchStale), now)
		if err != nil {
			continue
		}
		b, _, _ := s.store.Batch(ctx, br.batch.ID)
		if !owned {
			br.mu.Lock()
			br.lost = true
			br.mu.Unlock()
		}
		if !owned || b.Canceled {
			cancel()
			return
		}
	}
}

// runItem scans one schema through the pool, waiting for a free slot.
func (s *Scans) runItem(ctx context.Context, br *batchRun, i int, run func(context.Context) (struct{}, error)) {
	set := func(state, msg string) {
		br.mu.Lock()
		br.batch.Items[i].State, br.batch.Items[i].Error = state, msg
		br.mu.Unlock()
		s.save(br)
	}
	var job scanjobs.Job[struct{}]
	for {
		if ctx.Err() != nil {
			return
		}
		var err error
		if job, err = s.pool.Start(br.batch.Datasource, run); err == nil {
			break
		} else if !errors.Is(err, scanjobs.ErrBusy) {
			set("failed", err.Error())
			return
		}
		sleep(ctx, time.Second)
	}
	set("running", "")
	s.awaitItem(ctx, job.ID, set)
}

// awaitItem waits for the scan job of an item to end and records how; a
// batch stopped meanwhile cancels the job and leaves the item for whoever
// runs the batch next (or the end of the batch) to settle.
func (s *Scans) awaitItem(ctx context.Context, id string, set func(state, msg string)) {
	for {
		sleep(ctx, batchPoll)
		if ctx.Err() != nil {
			s.pool.Cancel(id)
			return
		}
		current, ok := s.pool.Get(id)
		switch {
		case !ok:
			set("failed", "the scan expired")
			return
		case current.State == scanjobs.Done:
			set("done", "")
			return
		case current.State == scanjobs.Failed:
			set("failed", current.Err)
			return
		case current.State == scanjobs.Canceled:
			set("canceled", "")
			return
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
// they did not scan yet is scanned here. runner says how to scan a
// datasource as configured now.
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
		now := s.now()
		if claimed, err := s.store.ClaimBatch(ctx, b.ID, s.batches.owner, now.Add(-batchStale), now); err != nil ||
			!claimed {
			continue
		}
		run, err := runner(ctx, b.Datasource)
		if err != nil {
			slog.Warn("scan batch not resumed", "batch", b.ID, "error", err.Error())
			continue
		}
		for i := range b.Items {
			if b.Items[i].State == "running" {
				b.Items[i].State = "pending"
			}
		}
		b.Owner, b.Heartbeat = s.batches.owner, now
		slog.Info("scan batch taken over", "batch", b.ID, "datasource", b.Datasource)
		s.runBatch(b, run)
	}
}

// WatchBatches takes over the batches other processes stopped running, now
// and every batchStale, until the scans close.
func (s *Scans) WatchBatches(runner func(ctx context.Context, datasource string) (scanRunner, error)) {
	s.batches.wg.Add(1)
	go func() {
		defer s.batches.wg.Done()
		for {
			s.takeOver(s.batches.ctx, runner)
			select {
			case <-s.batches.ctx.Done():
				return
			case <-time.After(batchStale):
			}
		}
	}()
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
func (r *Resolver) runnerFor(ctx context.Context, datasource string) (scanRunner, error) {
	scans, err := r.scans()
	if err != nil {
		return nil, err
	}
	cfg, err := r.selectConfig(ctx, nil, nil)
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
func (r *Resolver) WatchScanBatches() {
	if r.Scans != nil {
		r.Scans.WatchBatches(r.runnerFor)
	}
}

func (r *Resolver) startScanBatch(ctx context.Context, datasource string, schemas []string) (*ScanBatch, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	if len(schemas) == 0 {
		return nil, errors.New("name the schemas to scan")
	}
	_, scans, _, err := r.scanContext(ctx, datasource)
	if err != nil {
		return nil, err
	}
	run, err := r.runnerFor(ctx, datasource)
	if err != nil {
		return nil, err
	}
	return scans.startBatch(ctx, datasource, who.Username, slices.Compact(slices.Sorted(slices.Values(schemas))), run)
}

func (r *Resolver) scanBatches(ctx context.Context, datasource string) ([]ScanBatch, error) {
	if _, err := auth.Require(ctx, auth.PermWrite); err != nil {
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

// cancelScanBatch asks the owner of a batch to stop; this process stops at
// once a batch it runs.
func (r *Resolver) cancelScanBatch(ctx context.Context, id string) (*ScanBatch, error) {
	if _, err := auth.Require(ctx, auth.PermWrite); err != nil {
		return nil, err
	}
	scans, err := r.scans()
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

func (r *Resolver) retryScanBatch(ctx context.Context, id string) (*ScanBatch, error) {
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
	var again []string
	for _, item := range b.Items {
		if item.State == "failed" || item.State == "canceled" {
			again = append(again, item.Schema)
		}
	}
	if len(again) == 0 {
		return nil, errors.New("nothing in the batch failed or was canceled")
	}
	return r.startScanBatch(ctx, b.Datasource, again)
}
