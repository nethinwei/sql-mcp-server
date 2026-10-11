// Package revisionopstest stresses the revision operations on a store: many
// administrators editing and publishing at once, and the edge cases of the
// fast-forward rule. Every revision.Store runs it, the SQL ones against
// their databases.
package revisionopstest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/postgres" // publishing checks the driver
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// NewStore returns an empty store; the suite closes it.
type NewStore func(t *testing.T) revision.Store

// Options scales the concurrent cases.
type Options struct {
	// Admins publish at once, Rounds changes each; 8 and 4 when zero.
	Admins, Rounds int
}

// Run runs the suite against stores produced by newStore.
func Run(t *testing.T, newStore NewStore, opts Options) {
	t.Helper()
	if opts.Admins == 0 {
		opts.Admins = 8
	}
	if opts.Rounds == 0 {
		opts.Rounds = 4
	}
	cases := map[string]func(*testing.T, revisionops.Service){
		"NoLostPublishes":           func(t *testing.T, s revisionops.Service) { noLostPublishes(t, s, opts) },
		"RacingStaleDraftsOneWins":  func(t *testing.T, s revisionops.Service) { racingStaleDrafts(t, s, opts) },
		"PublishRacesRollback":      publishRacesRollback,
		"FirstPublish":              firstPublish,
		"DraftChain":                draftChain,
		"DraftAfterRollbackIsStale": draftAfterRollbackIsStale,
		"MissingParent":             missingParent,
		"PublishTwice":              publishTwice,
		"StaleExpectedPublished":    staleExpectedPublished,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			t.Cleanup(func() { _ = store.Close() })
			fn(t, revisionops.Service{Store: store, Via: "edge-test"})
		})
	}
}

// payload is a publishable configuration with one entity per name.
func payload(t *testing.T, names ...string) []byte {
	t.Helper()
	cfg := &config.Config{Databases: map[string]config.DatabaseConfig{
		"default": {Driver: "postgres", DSN: "postgres://edge"},
	}}
	for _, name := range names {
		cfg.Entities = append(cfg.Entities, config.EntityConfig{Name: name, Fields: []config.FieldConfig{{Name: "id"}}})
	}
	cfg.ApplyDefaults()
	out, err := revisionops.Normalize(config.SpecOf(cfg))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// entityNames are the entities of a payload.
func entityNames(t *testing.T, p []byte) map[string]bool {
	t.Helper()
	cfg, err := configyaml.Decode(p)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, e := range cfg.Entities {
		out[e.Name] = true
	}
	return out
}

// with is the published payload plus one entity.
func with(t *testing.T, current revision.Revision, name string) []byte {
	t.Helper()
	names := []string{name}
	for n := range entityNames(t, current.Payload) {
		names = append(names, n)
	}
	return payload(t, names...)
}

func mustDraft(t *testing.T, s revisionops.Service, p []byte, parent int64) revision.Revision {
	t.Helper()
	d, _, err := s.Draft(context.Background(), revisionops.DraftRequest{Payload: p, Parent: parent, Author: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mustPublish(t *testing.T, s revisionops.Service, id int64) revision.Revision {
	t.Helper()
	r, err := s.Publish(context.Background(), revisionops.PublishRequest{ID: id, Author: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func wantConflict(t *testing.T, err error, published int64) {
	t.Helper()
	var conflict *revisionops.ConflictError
	if !errors.As(err, &conflict) || conflict.Published != published {
		t.Fatalf("err = %v, want a conflict naming published revision %d", err, published)
	}
}

// noLostPublishes: administrators each read the published revision, add
// their own entity and publish, starting over from the newest revision on a
// conflict, as the console's rebase does. Every change must survive, and
// each publish must build on the one before it.
func noLostPublishes(t *testing.T, s revisionops.Service, opts Options) {
	ctx := context.Background()
	mustPublish(t, s, mustDraft(t, s, payload(t, "base"), 0).ID)
	var wg sync.WaitGroup
	errs := make(chan error, opts.Admins)
	for a := range opts.Admins {
		wg.Go(func() {
			for r := range opts.Rounds {
				if err := publishWithRetry(t, s, fmt.Sprintf("a%d_r%d", a, r)); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	current, err := s.Store.Published(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := entityNames(t, current.Payload)
	for a := range opts.Admins {
		for r := range opts.Rounds {
			if name := fmt.Sprintf("a%d_r%d", a, r); !got[name] {
				t.Errorf("the change %s published was lost", name)
			}
		}
	}
	assertLinearHistory(t, s, 1+opts.Admins*opts.Rounds)
}

// publishWithRetry publishes the published configuration plus name,
// rebasing on conflicts.
func publishWithRetry(t *testing.T, s revisionops.Service, name string) error {
	ctx := context.Background()
	for range 1000 {
		current, err := s.Store.Published(ctx)
		if err != nil {
			return err
		}
		d, _, err := s.Draft(ctx, revisionops.DraftRequest{Payload: with(t, current, name), Parent: current.ID})
		if err != nil {
			return err
		}
		expected := current.ID
		_, err = s.Publish(ctx, revisionops.PublishRequest{ID: d.ID, ExpectedPublished: &expected})
		if !errors.Is(err, revision.ErrConflict) {
			return err
		}
	}
	return fmt.Errorf("%s: no publish after 1000 rebases", name)
}

// assertLinearHistory checks that the published revisions, oldest first,
// each build on the one before, and that there are want of them.
func assertLinearHistory(t *testing.T, s revisionops.Service, want int) {
	t.Helper()
	list, err := s.Store.List(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var chain []revision.Revision
	byID := map[int64]revision.Revision{}
	for _, r := range list {
		byID[r.ID] = r
	}
	for _, r := range list {
		if r.State == revision.StatePublished || r.State == revision.StateSuperseded {
			chain = append(chain, r)
		}
	}
	sortByPublished(chain)
	if len(chain) != want {
		t.Fatalf("%d revisions were published, want %d", len(chain), want)
	}
	published := 0
	for i, r := range chain {
		if r.State == revision.StatePublished {
			published++
		}
		if i == 0 {
			continue
		}
		// The parent is the previous published revision (drafts here build
		// on the published one directly).
		if parent := byID[r.ParentID]; parent.ID != chain[i-1].ID {
			t.Errorf("revision %d builds on %d, but %d was published before it", r.ID, r.ParentID, chain[i-1].ID)
		}
	}
	if published != 1 {
		t.Errorf("%d revisions are published at once", published)
	}
}

func sortByPublished(rs []revision.Revision) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && less(rs[j], rs[j-1]); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

func less(a, b revision.Revision) bool {
	if !a.PublishedAt.Equal(b.PublishedAt) {
		return a.PublishedAt.Before(b.PublishedAt)
	}
	return a.ID < b.ID
}

// racingStaleDrafts: drafts all built on one published revision race to
// publish; exactly one wins and every other gets a conflict naming it.
func racingStaleDrafts(t *testing.T, s revisionops.Service, opts Options) {
	base := mustPublish(t, s, mustDraft(t, s, payload(t, "base"), 0).ID)
	drafts := make([]revision.Revision, opts.Admins)
	for i := range drafts {
		drafts[i] = mustDraft(t, s, with(t, base, fmt.Sprintf("d%d", i)), base.ID)
	}
	results := make([]error, len(drafts))
	var wg sync.WaitGroup
	for i, d := range drafts {
		wg.Go(func() {
			_, results[i] = s.Publish(context.Background(), revisionops.PublishRequest{ID: d.ID})
		})
	}
	wg.Wait()
	current, err := s.Store.Published(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wins := 0
	for i, err := range results {
		if err == nil {
			wins++
			continue
		}
		var conflict *revisionops.ConflictError
		if !errors.As(err, &conflict) || conflict.Published != current.ID {
			t.Errorf("draft %d: %v, want a conflict naming %d", drafts[i].ID, err, current.ID)
		}
	}
	if wins != 1 {
		t.Fatalf("%d stale drafts were published, want exactly 1", wins)
	}
	// Losers stay drafts and still cannot be published.
	for i, err := range results {
		if err != nil {
			_, err := s.Publish(context.Background(), revisionops.PublishRequest{ID: drafts[i].ID})
			wantConflict(t, err, current.ID)
		}
	}
}

// publishRacesRollback: publishes and rollbacks interleave; one revision is
// published at the end and the history stays consistent.
func publishRacesRollback(t *testing.T, s revisionops.Service) {
	ctx := context.Background()
	first := mustPublish(t, s, mustDraft(t, s, payload(t, "v1"), 0).ID)
	mustPublish(t, s, mustDraft(t, s, with(t, first, "v2"), first.ID).ID)
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			if i%2 == 0 {
				_, _ = s.Rollback(ctx, revisionops.RollbackRequest{To: first.ID, Author: "edge"})
				return
			}
			_ = publishWithRetry(t, s, fmt.Sprintf("p%d", i))
		})
	}
	wg.Wait()
	list, err := s.Store.List(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	for _, r := range list {
		if r.State == revision.StatePublished {
			published++
		}
	}
	if published != 1 {
		t.Fatalf("%d revisions are published after racing rollbacks", published)
	}
}

// firstPublish: with nothing published, any draft is publishable.
func firstPublish(t *testing.T, s revisionops.Service) {
	d := mustDraft(t, s, payload(t, "x"), 0)
	if d.ParentID != 0 {
		t.Fatalf("a draft of an empty store builds on %d", d.ParentID)
	}
	mustPublish(t, s, d.ID)
}

// draftChain: drafts built on drafts build on the published revision their
// chain starts from.
func draftChain(t *testing.T, s revisionops.Service) {
	p := mustPublish(t, s, mustDraft(t, s, payload(t, "x"), 0).ID)
	d := p
	for i := range 5 {
		d = mustDraft(t, s, with(t, d, fmt.Sprintf("c%d", i)), d.ID)
	}
	other := mustDraft(t, s, with(t, p, "other"), p.ID)
	mustPublish(t, s, d.ID)
	_, err := s.Publish(context.Background(), revisionops.PublishRequest{ID: other.ID})
	wantConflict(t, err, d.ID)
}

// draftAfterRollbackIsStale: a rollback publishes a new revision, so drafts
// built before it are stale.
func draftAfterRollbackIsStale(t *testing.T, s revisionops.Service) {
	ctx := context.Background()
	first := mustPublish(t, s, mustDraft(t, s, payload(t, "v1"), 0).ID)
	second := mustPublish(t, s, mustDraft(t, s, with(t, first, "v2"), first.ID).ID)
	stale := mustDraft(t, s, with(t, second, "v3"), second.ID)
	res, err := s.Rollback(ctx, revisionops.RollbackRequest{To: first.ID, Author: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Publish(ctx, revisionops.PublishRequest{ID: stale.ID})
	wantConflict(t, err, res.Published.ID)
}

// missingParent: a draft naming a revision that does not exist fails to
// publish with an error, not a panic or a publish.
func missingParent(t *testing.T, s revisionops.Service) {
	mustPublish(t, s, mustDraft(t, s, payload(t, "x"), 0).ID)
	d, err := s.Store.Create(context.Background(), revision.Draft{ParentID: 9999, Payload: payload(t, "y")})
	if err != nil {
		return // the store refuses it already
	}
	if _, err := s.Publish(context.Background(), revisionops.PublishRequest{ID: d.ID}); err == nil {
		t.Fatal("a draft built on a missing revision was published")
	}
}

// publishTwice: a published revision cannot be published again.
func publishTwice(t *testing.T, s revisionops.Service) {
	p := mustPublish(t, s, mustDraft(t, s, payload(t, "x"), 0).ID)
	if _, err := s.Publish(context.Background(), revisionops.PublishRequest{ID: p.ID}); err == nil {
		t.Fatal("a published revision was published again")
	}
}

// staleExpectedPublished: naming an older published revision fails even for
// a draft built on the current one.
func staleExpectedPublished(t *testing.T, s revisionops.Service) {
	first := mustPublish(t, s, mustDraft(t, s, payload(t, "v1"), 0).ID)
	second := mustPublish(t, s, mustDraft(t, s, with(t, first, "v2"), first.ID).ID)
	d := mustDraft(t, s, with(t, second, "v3"), second.ID)
	expected := first.ID
	_, err := s.Publish(context.Background(), revisionops.PublishRequest{ID: d.ID, ExpectedPublished: &expected})
	wantConflict(t, err, second.ID)
}
