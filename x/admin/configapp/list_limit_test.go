package configapp

import (
	"context"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
)

// listedStore records the limit Revisions asks the store for.
type listedStore struct {
	revision.Store
	limit int
}

func (s *listedStore) List(ctx context.Context, limit int) ([]revision.Revision, error) {
	s.limit = limit
	return s.Store.List(ctx, limit)
}

// A store lists everything for a limit of 0: a page is never unbounded.
func TestRevisionsPageIsBounded(t *testing.T) {
	t.Parallel()
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Permissions: []string{auth.PermAll}})
	store := &listedStore{Store: revision.NewMemoryStore(nil)}
	for _, payload := range []string{"a: 1\n", "a: 2\n", "a: 3\n"} {
		if _, err := store.Create(ctx, revision.Draft{Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
	}
	app := &Service{Store: store}
	for limit, want := range map[int]int{0: 1, -5: 1, 2: 2, maxListed + 1: maxListed} {
		revs, err := app.Revisions(ctx, limit)
		if err != nil || store.limit != want || len(revs) != min(want, 3) {
			t.Fatalf("Revisions(%d) = %d revisions, store limit %d, %v; want limit %d",
				limit, len(revs), store.limit, err, want)
		}
	}
	if got := listLimit(0); got != 1 {
		t.Fatalf("workspace log limit 0 = %d", got)
	}
}
