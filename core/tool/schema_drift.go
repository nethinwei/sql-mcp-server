package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/nethinwei/sql-mcp-server/core/store"
)

// SchemaDrift is the signal that a statement found the database's schema
// changed under the running configuration: a table or a column it named is
// gone. Whoever serves the configuration assembles it again on the signal,
// so the entities the change broke are reported unavailable (see
// entity.Entity.Unavailable). The zero value is ready; a nil one drops
// reports.
type SchemaDrift struct{ reported atomic.Bool }

// Report raises the signal; true when it was not raised already.
func (d *SchemaDrift) Report() bool {
	return d != nil && d.reported.CompareAndSwap(false, true)
}

// Take reports whether the signal was raised since the last Take, and
// lowers it.
func (d *SchemaDrift) Take() bool { return d != nil && d.reported.Swap(false) }

// Pending reports whether the signal is raised.
func (d *SchemaDrift) Pending() bool { return d != nil && d.reported.Load() }

// schemaChanged is the error of a statement of an entity whose table or
// column the database no longer has: the entity is unavailable, as if the
// configuration had been assembled after the change.
type schemaChanged struct {
	entity, why string
	cause       error
}

func (e *schemaChanged) Error() string {
	return fmt.Sprintf("%s: %q: %s", ErrEntityUnavailable, e.entity, e.why)
}
func (e *schemaChanged) Is(target error) bool { return target == ErrEntityUnavailable }
func (e *schemaChanged) Unwrap() error        { return e.cause }

// schemaChange classifies err when it is a schema change under the entity of
// st, and raises the drift signal; nil otherwise.
func (st statement) schemaChange(err error) error {
	return changedSchema(st.tc, st.res.Entity.Name, err)
}

// changedSchema is err as a schema change under entity, raising the drift
// signal, when the database reported a table or a column it named missing;
// nil otherwise. The cost gate's EXPLAIN meets the change before any
// statement does.
func changedSchema(tc Context, entity string, err error) error {
	why := ""
	switch {
	case errors.Is(err, store.ErrMissingRelation):
		why = "its table is missing from the database"
	case errors.Is(err, store.ErrMissingColumn):
		why = "a column it uses is missing from the database"
	default:
		return nil
	}
	if tc.Drift.Report() {
		slog.Warn("schema changed under the running configuration; assembling it again",
			"entity", entity, "detail", why)
	}
	return &schemaChanged{entity: entity, why: why, cause: err}
}

// schemaChangeOfCall is the error of a call, a schema change under its
// entity when it is one not classified yet (see changedSchema).
func schemaChangeOfCall(tc Context, toolName string, input json.RawMessage, err error) error {
	var changed *schemaChanged
	if err == nil || errors.As(err, &changed) {
		return err
	}
	if out := changedSchema(tc, entityNameForTool(toolName, input, tc.Registry), err); out != nil {
		return out
	}
	return err
}

// retryStalePlan runs do again when the database refused it for a stale
// cached plan, which it dropped: the statement did not run, and is planned
// afresh. Inside a transaction the refusal ended it (see
// TransactionManager), so nothing is retried there.
func retryStalePlan[T any](tc Context, do func() (T, error)) (T, error) {
	out, err := do()
	if err != nil && tc.Transaction == "" && errors.Is(err, store.ErrStalePlan) {
		return do()
	}
	return out, err
}
