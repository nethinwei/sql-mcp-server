// Package revision defines the persisted configuration revision model and the
// Store boundary. A revision holds one complete, normalized configuration
// payload; content is immutable once written and only the state and publish
// time change. At most one revision is published at a time.
package revision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// State is a revision lifecycle state.
type State string

// Revision states.
const (
	StateDraft      State = "draft"
	StatePublished  State = "published"
	StateSuperseded State = "superseded"
	StateRolledBack State = "rolled-back"
)

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	switch s {
	case StateDraft, StatePublished, StateSuperseded, StateRolledBack:
		return true
	}
	return false
}

// Store errors.
var (
	ErrNotFound     = errors.New("revision: not found")
	ErrNoPublished  = errors.New("revision: no published revision")
	ErrConflict     = errors.New("revision: published revision changed concurrently")
	ErrInvalidState = errors.New("revision: invalid state transition")
	ErrHashMismatch = errors.New("revision: content hash mismatch")
	ErrNoRollback   = errors.New("revision: no earlier published revision to roll back to")
	ErrNotEmpty     = errors.New("revision: store is not empty")
)

// Revision is one persisted configuration revision. ParentID 0 means none;
// a zero PublishedAt means the revision has never been published.
type Revision struct {
	ID          int64
	ParentID    int64
	ContentHash string
	Payload     []byte
	State       State
	Author      string
	Comment     string
	CreatedAt   time.Time
	PublishedAt time.Time
}

// Verify recomputes the content hash and fails closed on a mismatch.
func (r Revision) Verify() error {
	if got := Hash(r.Payload); got != r.ContentHash {
		return fmt.Errorf("%w: revision %d stores %s, payload hashes to %s", ErrHashMismatch, r.ID, r.ContentHash, got)
	}
	return nil
}

// Summary returns r without its payload, as listed by Store.List.
func (r Revision) Summary() Revision {
	r.Payload = nil
	return r
}

// Draft is the input for a new draft revision.
type Draft struct {
	ParentID int64
	Payload  []byte
	Author   string
	Comment  string
}

// Meta records who performed a publish or rollback and why.
type Meta struct {
	Author  string
	Comment string
}

// Store persists revisions. Implementations must be safe for concurrent use
// and perform each state transition atomically.
type Store interface {
	// Create writes a new draft and returns it with its ID and hash.
	Create(ctx context.Context, d Draft) (Revision, error)
	// Get returns one revision with its payload.
	Get(ctx context.Context, id int64) (Revision, error)
	// List returns revision summaries, newest first; limit <= 0 means all.
	List(ctx context.Context, limit int) ([]Revision, error)
	// Published returns the published revision, or ErrNoPublished.
	Published(ctx context.Context) (Revision, error)
	// Publish publishes draft id. expectedCurrent is the caller's view of the
	// published ID (0 for none); a mismatch fails with ErrConflict.
	Publish(ctx context.Context, id, expectedCurrent int64, m Meta) (Revision, error)
	// Rollback marks the published revision rolled-back and publishes a new
	// revision copying target's payload. Target 0 selects the newest
	// superseded revision whose content differs from the published one.
	Rollback(ctx context.Context, expectedCurrent, target int64, m Meta) (Revision, error)
	// ImportHistory copies revisions verbatim into an empty store, keeping
	// IDs, states, authors and timestamps. It is used by migrate --history.
	ImportHistory(ctx context.Context, revs []Revision) error
	Close() error
}

// HashPrefix prefixes every content hash.
const HashPrefix = "sha256:"

// Hash returns the content hash of a payload.
func Hash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return HashPrefix + hex.EncodeToString(sum[:])
}

// ValidateHistory checks revisions for ImportHistory: unique positive IDs,
// known states, matching hashes, and at most one published revision.
func ValidateHistory(revs []Revision) error {
	seen := make(map[int64]bool, len(revs))
	published := 0
	for _, r := range revs {
		if r.ID <= 0 || seen[r.ID] {
			return fmt.Errorf("%w: invalid or duplicate revision id %d", ErrInvalidState, r.ID)
		}
		seen[r.ID] = true
		if !r.State.Valid() {
			return fmt.Errorf("%w: revision %d has unknown state %q", ErrInvalidState, r.ID, r.State)
		}
		if err := r.Verify(); err != nil {
			return err
		}
		if r.State == StatePublished {
			published++
		}
	}
	if published > 1 {
		return fmt.Errorf("%w: %d published revisions", ErrInvalidState, published)
	}
	return nil
}

// RollbackTarget selects the rollback target among revs for the published
// revision current: an explicit target must exist and not be a draft;
// otherwise the newest superseded revision whose content differs is chosen.
func RollbackTarget(revs []Revision, current Revision, target int64) (Revision, error) {
	if target != 0 {
		for _, r := range revs {
			if r.ID == target {
				if r.State == StateDraft {
					return Revision{}, fmt.Errorf("%w: rollback target %d is a draft", ErrInvalidState, target)
				}
				return r, nil
			}
		}
		return Revision{}, fmt.Errorf("%w: rollback target %d", ErrNotFound, target)
	}
	var best Revision
	for _, r := range revs {
		if r.State != StateSuperseded || r.ContentHash == current.ContentHash {
			continue
		}
		if best.ID == 0 || r.PublishedAt.After(best.PublishedAt) ||
			r.PublishedAt.Equal(best.PublishedAt) && r.ID > best.ID {
			best = r
		}
	}
	if best.ID == 0 {
		return Revision{}, ErrNoRollback
	}
	return best, nil
}
