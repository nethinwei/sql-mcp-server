package configapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/scanjobs"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
	"github.com/nethinwei/sql-mcp-server/x/leasing"
)

// Scan batches scan many schemas of a datasource on the server, a few at a
// time through the scan pool; they are leased across processes by
// x/leasing: a batch goes on when the console that started it is closed,
// every replica shows it, and a process that stops is taken over.

const (
	batchPoll = 200 * time.Millisecond
	// batchList bounds the batches listed per datasource.
	batchList = 10
	// batchOpen bounds the unfinished batches of a datasource and
	// batchSchemas the schemas of one, so requests cannot pile up work.
	batchOpen    = 4
	batchSchemas = 5000
)

// scanRunner scans one schema of a datasource.
type scanRunner func(schema string) func(context.Context) (struct{}, error)

// work scans the items of a batch with run, through the pool.
func (s *Scans) work(run scanRunner) leasing.Work {
	return func(ctx context.Context, datasource, schema string) (string, string) {
		return s.scan(ctx, datasource, run(schema))
	}
}

// startBatch records a batch of schemas and runs it; a request sent again
// with its key gets the batch it started.
func (s *Scans) startBatch(
	ctx context.Context, datasource, who, key string, schemas []string, run scanRunner,
) (*ScanBatch, error) {
	b := catalog.Batch{
		Datasource: datasource, StartedBy: who, RequestKey: key, Items: make([]catalog.BatchItem, 0, len(schemas)),
	}
	for _, schema := range schemas {
		b.Items = append(b.Items, catalog.BatchItem{Schema: schema, State: catalog.ItemPending})
	}
	got, started, err := s.batches.Start(ctx, b, batchOpen, s.work(run))
	if errors.Is(err, catalog.ErrTooManyBatches) {
		return nil, fmt.Errorf("%s has %d scan batches running: wait for them or stop one", datasource, batchOpen)
	}
	if err != nil {
		return nil, err
	}
	same := func(it catalog.BatchItem, s string) bool { return it.Schema == s }
	if !started && !slices.EqualFunc(got.Items, schemas, same) {
		// The key named another request: its batch would leave these schemas unscanned.
		return nil, fmt.Errorf("request key %q started a batch of other schemas: send a new key", key)
	}
	return viewBatch(got), nil
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

// WatchBatches takes over the batches other processes stopped running, now
// and then, until the scans close. runner says how to scan a datasource as
// configured now.
func (s *Scans) WatchBatches(runner func(ctx context.Context, datasource string) (scanRunner, error)) {
	s.batches.Watch(func(ctx context.Context, datasource string) (leasing.Work, error) {
		run, err := runner(ctx, datasource)
		if err != nil {
			return nil, err
		}
		return s.work(run), nil
	})
}

// viewBatch shows b with its items when they were read (Batch), otherwise
// (the lists) with their counts only, Items nil.
func viewBatch(b catalog.Batch) *ScanBatch {
	out := &ScanBatch{
		ID: b.ID, Datasource: b.Datasource, State: "running", StartedBy: b.StartedBy, StartedAt: b.StartedAt,
	}
	if !b.FinishedAt.IsZero() {
		out.State, out.FinishedAt = "done", &b.FinishedAt
	}
	counts := b.Counts
	if b.Items != nil {
		counts = map[string]int{}
		out.Items = make([]ScanBatchItem, 0, len(b.Items))
		for _, item := range b.Items {
			counts[item.State]++
			out.Items = append(
				out.Items,
				ScanBatchItem{Schema: item.Schema, State: item.State, Error: kit.Optional(item.Error)},
			)
		}
	}
	for state, n := range counts {
		out.Total += n
		switch state {
		case "done":
			out.Done += n
		case "failed":
			out.Failed += n
		case "canceled":
			out.Canceled += n
		default:
			out.Pending += n
		}
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
	cfg, err := r.SelectConfig(ctx, Against{})
	if err != nil {
		return nil, err
	}
	if !hasDatasource(cfg, datasource) {
		return nil, fmt.Errorf("%w: unknown datasource %q", leasing.ErrGone, datasource)
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
	datasource, key := in.Datasource, strings.TrimSpace(kit.Deref(in.RequestKey))
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

// ScanBatches lists the recent batches of datasource, running ones first,
// without their items (see ScanBatch): a console polls it.
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
	return viewBatches(all), nil
}

// ScanBatchesWithItems lists the recent batches of a datasource as
// ScanBatches does, with their items.
func (r *Service) ScanBatchesWithItems(ctx context.Context, datasource string) ([]ScanBatch, error) {
	if _, err := auth.RequireDatasource(ctx, auth.PermWrite, datasource); err != nil {
		return nil, err
	}
	scans, err := r.scans()
	if err != nil {
		return nil, err
	}
	all, err := scans.store.BatchesWithItems(ctx, datasource, batchList)
	if err != nil {
		return nil, err
	}
	return viewBatches(all), nil
}

// viewBatches shows batches, the running ones first.
func viewBatches(all []catalog.Batch) []ScanBatch {
	out := make([]ScanBatch, 0, len(all))
	for _, b := range all {
		out = append(out, *viewBatch(b))
	}
	slices.SortStableFunc(
		out,
		func(a, b ScanBatch) int { return boolRank(a.State != "running") - boolRank(b.State != "running") },
	)
	return out
}

// ScanBatch is batch id with its items.
func (r *Service) ScanBatch(ctx context.Context, id string) (*ScanBatch, error) {
	_, b, err := r.batchOf(ctx, id)
	if err != nil {
		return nil, err
	}
	return viewBatch(b), nil
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
	scans, _, err := r.batchOf(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := scans.store.CancelBatch(ctx, id); err != nil {
		return nil, err
	}
	scans.batches.Stop(id)
	b, ok, err := scans.store.Batch(ctx, id)
	if err != nil || !ok {
		return nil, err
	}
	return viewBatch(b), nil
}

// batchOf reads a batch, checking the caller may act on its datasource.
func (r *Service) batchOf(ctx context.Context, id string) (*Scans, catalog.Batch, error) {
	if _, err := auth.Require(ctx, auth.PermWrite); err != nil {
		return nil, catalog.Batch{}, err
	}
	scans, err := r.scans()
	if err != nil {
		return nil, catalog.Batch{}, err
	}
	b, ok, err := scans.store.Batch(ctx, id)
	if err != nil {
		return nil, catalog.Batch{}, err
	}
	if !ok {
		return nil, catalog.Batch{}, fmt.Errorf("unknown scan batch %q", id)
	}
	if _, err := auth.RequireDatasource(ctx, auth.PermWrite, b.Datasource); err != nil {
		return nil, catalog.Batch{}, err
	}
	return scans, b, nil
}

// RetryScanBatch scans again, as a new batch, what failed or was canceled
// in a batch.
func (r *Service) RetryScanBatch(ctx context.Context, id string) (*ScanBatch, error) {
	_, b, err := r.batchOf(ctx, id)
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
