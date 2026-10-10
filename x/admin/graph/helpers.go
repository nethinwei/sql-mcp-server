package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefBool(b *bool) bool { return b != nil && *b }

// withPayload returns obj's revision, fetching the payload when obj came
// from a payload-free listing.
func (r *Resolver) withPayload(ctx context.Context, obj *Revision) (revision.Revision, error) {
	if obj.rev.Payload != nil {
		return obj.rev, nil
	}
	return r.App.Store.Get(ctx, obj.rev.ID)
}

// selectConfig selects a configuration: an unsaved draft, a revision, or,
// when both are nil, the published revision.
func (r *Resolver) selectConfig(ctx context.Context, draft *DraftInput, id *string) (*config.Config, error) {
	d, err := toDraft(draft)
	if err != nil {
		return nil, err
	}
	return r.App.SelectConfig(ctx, d, id)
}

// apiError maps service errors that the console resolves itself to a
// GraphQL error with extensions.code: a publish conflict becomes CONFLICT
// with the newly published revision.
func apiError(err error) error {
	var conflict *revisionops.ConflictError
	if errors.As(err, &conflict) {
		return &gqlerror.Error{
			Message:    err.Error(),
			Extensions: map[string]any{"code": "CONFLICT", "published": strconv.FormatInt(conflict.Published, 10)},
		}
	}
	if errors.Is(err, configstore.ErrTemplateConflict) || errors.Is(err, configstore.ErrTemplateExists) ||
		errors.Is(err, configapp.ErrPlanChanged) {
		return &gqlerror.Error{Message: err.Error(), Extensions: map[string]any{"code": "CONFLICT"}}
	}
	return err
}

func serverStatus(status func() RuntimeState) *ServerStatus {
	if status == nil {
		return &ServerStatus{}
	}
	st := status()
	out := &ServerStatus{Watching: st.Watching}
	if st.Applied > 0 {
		id := strconv.FormatInt(st.Applied, 10)
		out.AppliedRevision = &id
	}
	if st.Pending != nil {
		out.Pending = &PendingRevision{
			ID: strconv.FormatInt(st.Pending.RevisionID, 10), RestartRequired: st.Pending.RestartRequired,
			Error: st.Pending.Err,
		}
	}
	if st.Execution != nil {
		out.Execution = executionStatus(*st.Execution)
	}
	return out
}

func executionStatus(st bus.Stats) *ExecutionStatus {
	slot := func(s bus.SlotStats) *SlotStatus {
		return &SlotStatus{Size: s.Size, Used: s.Used, Waiting: s.Waiting}
	}
	out := &ExecutionStatus{Io: slot(st.IO), Aux: slot(st.Aux), Lanes: []LaneStatus{}, Tasks: []TaskStatus{}}
	for _, l := range st.Lanes {
		out.Lanes = append(out.Lanes, LaneStatus{
			Name: string(l.Name), MaxConcurrent: l.MaxConcurrent, MaxQueue: l.MaxQueue, Inflight: l.Inflight,
			Queued: l.Queued, Rejected: int(l.Rejected), Completed: int(l.Completed),
		})
	}
	for _, t := range st.Tasks {
		out.Tasks = append(out.Tasks, TaskStatus{Name: t.Name, Running: t.Running})
	}
	return out
}

// configSchema returns the configuration JSON Schema as a JSON value.
func configSchema() (any, error) {
	var schema any
	if err := json.Unmarshal(config.Schema(), &schema); err != nil {
		return nil, fmt.Errorf("decode config schema: %w", err)
	}
	return schema, nil
}

func derefInt(n *int, fallback int) int {
	if n == nil {
		return fallback
	}
	return *n
}
