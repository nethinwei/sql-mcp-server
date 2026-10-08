package revision_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/revision/revisiontest"
)

func TestMemoryStoreConformance(t *testing.T) {
	revisiontest.Run(t, func(*testing.T) revision.Store { return revision.NewMemoryStore(nil) })
}

func TestVerifyDetectsTampering(t *testing.T) {
	r := revision.Revision{ID: 1, Payload: []byte("a"), ContentHash: revision.Hash([]byte("a"))}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	r.Payload = []byte("b")
	if err := r.Verify(); !errors.Is(err, revision.ErrHashMismatch) {
		t.Fatalf("Verify = %v", err)
	}
}

func TestValidateHistoryRejectsInvalidInput(t *testing.T) {
	ok := func(id int64, s revision.State) revision.Revision {
		return revision.Revision{ID: id, State: s, Payload: []byte("p"), ContentHash: revision.Hash([]byte("p")),
			CreatedAt: time.Unix(0, 0)}
	}
	cases := map[string][]revision.Revision{
		"duplicate id":  {ok(1, revision.StateDraft), ok(1, revision.StateDraft)},
		"non-positive":  {ok(0, revision.StateDraft)},
		"unknown state": {ok(1, "weird")},
		"two published": {ok(1, revision.StatePublished), ok(2, revision.StatePublished)},
	}
	for name, revs := range cases {
		if err := revision.ValidateHistory(revs); !errors.Is(err, revision.ErrInvalidState) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
