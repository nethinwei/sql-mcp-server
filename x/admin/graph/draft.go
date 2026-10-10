package graph

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// ops returns the revision operations shared with the `store` CLI.
func (r *Resolver) ops() revisionops.Service { return r.App.Ops() }

func (r *Resolver) revisionByID(ctx context.Context, id string) (revision.Revision, error) {
	return r.App.Revision(ctx, id)
}

// draft is a merged, validated and normalized configuration.
type draft struct {
	base    revision.Revision
	cfg     *config.Config
	payload []byte
}

// toDraft is the configuration service's draft of a draft input.
func toDraft(in *DraftInput) (*configapp.Draft, error) {
	if in == nil {
		return nil, nil
	}
	edit, err := toEdit(*in)
	if err != nil {
		return nil, err
	}
	return &configapp.Draft{Base: in.Base, Edit: edit}, nil
}

// buildDraft applies in to its base revision; see configapp.BuildDraft.
func (r *Resolver) buildDraft(ctx context.Context, in DraftInput) (draft, error) {
	d, err := toDraft(&in)
	if err != nil {
		return draft{}, err
	}
	built, err := r.App.BuildDraft(ctx, *d)
	if err != nil {
		return draft{}, err
	}
	return draft{base: built.Base, cfg: built.Config, payload: built.Payload}, nil
}

// WorkspaceEdit reads the sections of a workspace, as the console keeps them
// (its entities, roles, users and settings in the input types it edits), as
// an edit of the workspace's base. The users are as they are, token
// included: one without a token hash has none (in a draft input, a missing
// hash keeps the base user's).
func WorkspaceEdit(sections []byte) (configedit.Edit, error) {
	var in DraftInput
	if err := json.Unmarshal(sections, &in); err != nil {
		return configedit.Edit{}, fmt.Errorf("workspace: %w", err)
	}
	none := ""
	for i := range in.Users {
		if in.Users[i].TokenHash == nil {
			in.Users[i].TokenHash = &none
		}
	}
	return toEdit(in)
}

// WorkspaceSections writes a configuration as a workspace's sections, as
// the console builds them from a revision (revisionSections in
// web/admin/src/stores/workspace.ts): the read model without what the
// console does not edit (an entity's id, a role's members, whether a user
// has a token) and with allowCascade only when set.
// testdata/workspace_sections.json keeps the two in step.
func WorkspaceSections(cfg *config.Config, payload []byte) ([]byte, error) {
	c, err := toConfiguration(cfg, payload)
	if err != nil {
		return nil, err
	}
	sections := map[string]any{"settings": c.Settings}
	for key, list := range map[string]any{"entities": c.Entities, "roles": c.Roles, "users": c.Users} {
		var items []map[string]any
		if err := remarshal(list, &items); err != nil {
			return nil, err
		}
		for _, it := range items {
			delete(it, "id")
			delete(it, "members")
			delete(it, "hasToken")
			if it["allowCascade"] == false {
				delete(it, "allowCascade")
			}
		}
		sections[key] = orEmpty(items)
	}
	return json.Marshal(sections)
}

// remarshal copies v into out through JSON.
func remarshal(v, out any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
