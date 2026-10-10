package configapp

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
)

// A configuration as workspace items is the console's sections, item by
// item: each entity (keyed by its ID), each role and each user (by name),
// and the settings as one item. The values are the types the console edits
// (the admin API provides the encoding, Service.WorkspaceSections and
// WorkspaceEdit).

// The kinds of workspace items, as the console names its changes.
const (
	ItemEntity   = "entity"
	ItemRole     = "role"
	ItemUser     = "user"
	ItemSettings = "settings"
)

// Schema is how the configuration's values merge: list elements by name
// (fields, relationships), else by the entity they grant; empty text of
// these properties means unset (an empty token hash: no token, as a user
// without one).
var Schema = workspace.Schema{
	ListKeys:     []string{"name", "entity"},
	OptionalText: []string{"source", "datasource", "schema", "kind", "description", "alias", "mask", "tokenHash"},
}

// sectionsDoc is the console's sections.
type sectionsDoc struct {
	Entities []json.RawMessage `json:"entities"`
	Roles    []json.RawMessage `json:"roles"`
	Users    []json.RawMessage `json:"users"`
	Settings json.RawMessage   `json:"settings"`
}

var sectionKinds = []struct {
	kind string
	list func(*sectionsDoc) *[]json.RawMessage
}{
	{ItemEntity, func(d *sectionsDoc) *[]json.RawMessage { return &d.Entities }},
	{ItemRole, func(d *sectionsDoc) *[]json.RawMessage { return &d.Roles }},
	{ItemUser, func(d *sectionsDoc) *[]json.RawMessage { return &d.Users }},
}

// itemKey is the key of an item of kind.
func itemKey(kind string, raw json.RawMessage) (string, error) {
	var id struct {
		Name       string `json:"name"`
		Datasource string `json:"datasource"`
		Schema     string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &id); err != nil || id.Name == "" {
		return "", fmt.Errorf("a workspace %s needs a name", kind)
	}
	if kind == ItemEntity {
		return entity.ID(id.Datasource, id.Schema, id.Name), nil
	}
	return id.Name, nil
}

// itemsOfSections are the items of the console's sections, in order.
func itemsOfSections(data []byte) (workspace.Snapshot, error) {
	var d sectionsDoc
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("workspace sections: %w", err)
	}
	out := workspace.Snapshot{}
	for _, sk := range sectionKinds {
		for _, raw := range *sk.list(&d) {
			key, err := itemKey(sk.kind, raw)
			if err != nil {
				return nil, err
			}
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, err
			}
			out = append(out, workspace.Item{Ref: workspace.Ref{Kind: sk.kind, Key: key}, Value: v})
		}
	}
	settings := map[string]any{}
	if len(d.Settings) > 0 && string(d.Settings) != "null" {
		if err := json.Unmarshal(d.Settings, &settings); err != nil {
			return nil, err
		}
	}
	return append(out, workspace.Item{Ref: workspace.Ref{Kind: ItemSettings, Key: ItemSettings}, Value: settings}), nil
}

// sectionsOfItems are the console's sections of items.
func sectionsOfItems(items workspace.Snapshot) ([]byte, error) {
	d := sectionsDoc{Entities: []json.RawMessage{}, Roles: []json.RawMessage{}, Users: []json.RawMessage{},
		Settings: json.RawMessage(`{}`)}
	for _, it := range items {
		raw, err := json.Marshal(it.Value)
		if err != nil {
			return nil, err
		}
		if it.Kind == ItemSettings {
			d.Settings = raw
			continue
		}
		for _, sk := range sectionKinds {
			if sk.kind == it.Kind {
				*sk.list(&d) = append(*sk.list(&d), raw)
			}
		}
	}
	return json.Marshal(d)
}

// codec is the configuration's workspaceops.Codec.
type codec struct{ r *Service }

func (c codec) ready() error {
	if c.r.WorkspaceEdit == nil || c.r.WorkspaceSections == nil {
		return errors.New("workspaces cannot be read here")
	}
	return nil
}

// Items implements workspaceops.Codec.
func (c codec) Items(rev revision.Revision) (workspace.Snapshot, error) {
	cfg, err := bootstrap.LoadRevision(rev)
	if err != nil {
		return nil, err
	}
	return c.itemsOf(cfg, rev.Payload)
}

// itemsOf are cfg's items (payload its encoding).
func (c codec) itemsOf(cfg *config.Config, payload []byte) (workspace.Snapshot, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	data, err := c.r.WorkspaceSections(cfg, payload)
	if err != nil {
		return nil, err
	}
	return itemsOfSections(data)
}

// edit is items as an edit of a base revision.
func (c codec) edit(items workspace.Snapshot) (configedit.Edit, error) {
	if err := c.ready(); err != nil {
		return configedit.Edit{}, err
	}
	data, err := sectionsOfItems(items)
	if err != nil {
		return configedit.Edit{}, err
	}
	return c.r.WorkspaceEdit(data)
}

// Build implements workspaceops.Codec: the configuration items make on
// base, checked as a whole.
func (c codec) Build(base revision.Revision, items workspace.Snapshot) ([]byte, error) {
	edit, err := c.edit(items)
	if err != nil {
		return nil, err
	}
	if err := c.removeSettings(base, &edit); err != nil {
		return nil, err
	}
	_, payload, err := configedit.Apply(base.Payload, edit)
	return payload, err
}

// removeSettings makes the settings item the settings as they are: the edit
// overlays base key by key, so a key base has and the item does not is
// removed (null), its default applying.
func (c codec) removeSettings(base revision.Revision, edit *configedit.Edit) error {
	was, err := c.Items(base)
	if err != nil {
		return err
	}
	settings, _ := was.Get(workspace.Ref{Kind: ItemSettings, Key: ItemSettings}).(map[string]any)
	for key := range settings {
		if _, kept := edit.Settings[key]; !kept {
			if edit.Settings == nil {
				edit.Settings = map[string]any{}
			}
			edit.Settings[key] = nil
		}
	}
	return nil
}
