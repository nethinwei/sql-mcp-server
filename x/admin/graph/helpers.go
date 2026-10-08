package graph

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
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

// newUserToken returns a 256-bit random bearer token in the same form as
// `sql-mcp-server user token`.
func newUserToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "smcp_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// auditAdmin logs a store mutation made through the admin API.
func hasDatasource(cfg *config.Config, name string) bool {
	if len(cfg.Databases) == 0 {
		return name == "default" && cfg.Database.Driver != ""
	}
	_, ok := cfg.Databases[name]
	return ok
}

// withPayload returns obj's revision, fetching the payload when obj came
// from a payload-free listing.
func (r *Resolver) withPayload(ctx context.Context, obj *Revision) (revision.Revision, error) {
	if obj.rev.Payload != nil {
		return obj.rev, nil
	}
	return r.Store.Get(ctx, obj.rev.ID)
}

// selectConfig selects a configuration: an unsaved draft, a revision, or,
// when both are nil, the published revision.
func (r *Resolver) selectConfig(ctx context.Context, draft *DraftInput, id *string) (*config.Config, error) {
	if draft != nil && id != nil {
		return nil, errors.New("pass either draft or revision, not both")
	}
	if draft != nil {
		d, err := r.buildDraft(ctx, *draft)
		if err != nil {
			return nil, err
		}
		return d.cfg, nil
	}
	var rev revision.Revision
	var err error
	if id != nil {
		rev, err = r.revisionByID(ctx, *id)
	} else {
		rev, err = r.Store.Published(ctx)
	}
	if err != nil {
		return nil, err
	}
	return bootstrap.LoadRevision(rev)
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
