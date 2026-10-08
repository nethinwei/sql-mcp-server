package graph

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
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
func auditAdmin(ctx context.Context, op string, rev revision.Revision) {
	p, _ := auth.PrincipalFrom(ctx)
	slog.Info("config store "+op, "op", op, "revision", rev.ID, "contentHash", rev.ContentHash,
		"author", p.Username, "via", "admin-api")
}

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

// simulationConfig selects the configuration to simulate against: an unsaved
// draft, a revision, or the published revision.
func (r *Resolver) simulationConfig(ctx context.Context, draft *DraftInput, id *string) (*config.Config, error) {
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

// codeConflict marks an error the client resolves by rebasing on the newly
// published revision.
const codeConflict = "CONFLICT"

// checkExpectedPublished fails with code CONFLICT when the published revision
// is no longer the one the caller based its work on.
func checkExpectedPublished(current revision.Revision, expected *string) error {
	if expected == nil {
		return nil
	}
	want, err := parseID(*expected)
	if err != nil {
		return err
	}
	if want == current.ID {
		return nil
	}
	return &gqlerror.Error{
		Message:    fmt.Sprintf("%v: revision %d was published after revision %d", revision.ErrConflict, current.ID, want),
		Extensions: map[string]any{"code": codeConflict, "published": strconv.FormatInt(current.ID, 10)},
	}
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
