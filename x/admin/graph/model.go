package graph

import (
	"strconv"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/revision"
)

// Revision is the GraphQL Revision. The payload stays private: config and
// yaml are resolved from it only when a query selects them.
type Revision struct {
	ID          string
	Parent      *string
	State       RevisionState
	ContentHash string
	Author      string
	Comment     string
	CreatedAt   time.Time
	PublishedAt *time.Time

	rev revision.Revision
}

var revisionStates = map[revision.State]RevisionState{
	revision.StateDraft:      RevisionStateDraft,
	revision.StatePublished:  RevisionStatePublished,
	revision.StateSuperseded: RevisionStateSuperseded,
	revision.StateRolledBack: RevisionStateRolledBack,
}

func toRevision(r revision.Revision) *Revision {
	out := &Revision{
		ID: strconv.FormatInt(r.ID, 10), State: revisionStates[r.State], ContentHash: r.ContentHash,
		Author: r.Author, Comment: r.Comment, CreatedAt: r.CreatedAt, rev: r,
	}
	if r.ParentID != 0 {
		parent := strconv.FormatInt(r.ParentID, 10)
		out.Parent = &parent
	}
	if !r.PublishedAt.IsZero() {
		published := r.PublishedAt
		out.PublishedAt = &published
	}
	return out
}
