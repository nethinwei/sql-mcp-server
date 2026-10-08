package revisionops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/postgres" // CheckPublishable checks drivers
)

const base = `database: {driver: postgres, dsn: x}
entities:
  - name: orders
    fields:
      - name: id
`

// payload returns base with maxRows set, in the store encoding.
func payload(t *testing.T, maxRows string) []byte {
	t.Helper()
	return Reencode([]byte(base + "cost: {maxRows: " + maxRows + "}\n"))
}

func newService(t *testing.T) (Service, revision.Revision) {
	t.Helper()
	s := Service{Store: revision.NewMemoryStore(nil), Via: "test"}
	d, _, err := s.Draft(context.Background(), DraftRequest{Payload: payload(t, "100")})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Publish(context.Background(), PublishRequest{ID: d.ID})
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func TestDraftSkipsUnchangedPayload(t *testing.T) {
	t.Parallel()
	s, published := newService(t)
	rev, unchanged, err := s.Draft(context.Background(), DraftRequest{Payload: payload(t, "100"), SkipIfPublished: true})
	if err != nil || !unchanged || rev.ID != published.ID {
		t.Fatalf("unchanged draft = %+v, %v, %v", rev, unchanged, err)
	}
	rev, unchanged, err = s.Draft(context.Background(), DraftRequest{Payload: payload(t, "200")})
	if err != nil || unchanged || rev.ParentID != published.ID {
		t.Fatalf("draft = %+v, %v, %v; parent must default to the published revision", rev, unchanged, err)
	}
}

func TestPublishChecksExpectedPublished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, published := newService(t)
	a, _, _ := s.Draft(ctx, DraftRequest{Payload: payload(t, "200")})
	b, _, _ := s.Draft(ctx, DraftRequest{Payload: payload(t, "300")})
	expected := published.ID
	if _, err := s.Publish(ctx, PublishRequest{ID: a.ID, ExpectedPublished: &expected}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Publish(ctx, PublishRequest{ID: b.ID, ExpectedPublished: &expected})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, revision.ErrConflict) || conflict.Published != a.ID {
		t.Fatalf("stale publish err = %v", err)
	}
}

func TestRollbackRestoresEarlierContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, first := newService(t)
	d, _, _ := s.Draft(ctx, DraftRequest{Payload: payload(t, "200")})
	if _, err := s.Publish(ctx, PublishRequest{ID: d.ID}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Rollback(ctx, RollbackRequest{Comment: "undo"})
	if err != nil || res.Target != first.ID || res.From != d.ID || res.Published.ContentHash != first.ContentHash {
		t.Fatalf("rollback = %+v, %v", res, err)
	}
}

func TestDiffIgnoresOlderEncodingOfEmptyValues(t *testing.T) {
	t.Parallel()
	old := []byte(`database: {driver: postgres, dsn: x}
entities:
  - name: orders
    source: orders
    schema: ""
    fields:
      - name: id
        alias: ""
        description: ""
        mask: ""
        exclude: false
    params: []
    fieldACL: {}
`)
	current := []byte(`database: {driver: postgres, dsn: x}
entities:
  - name: orders
    source: orders
    fields:
      - name: id
`)
	diff, err := Diff("old", old, "current", current)
	if err != nil {
		t.Fatal(err)
	}
	if diff != "" {
		t.Fatalf("encoding-only difference shows as a change:\n%s", diff)
	}
	edited := strings.Replace(string(current), "id\n", "id\n        alias: oid\n", 1)
	changed, err := Diff("old", old, "current", []byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(changed, "+        alias: oid") {
		t.Fatalf("real change missing from diff:\n%s", changed)
	}
}

func TestRawDiffComparesBytes(t *testing.T) {
	t.Parallel()
	diff, err := RawDiff("a", []byte("x: 1\n"), "b", []byte("x:   1\n"))
	if err != nil || !strings.Contains(diff, "+x:   1") {
		t.Fatalf("raw diff = %q, %v", diff, err)
	}
}
