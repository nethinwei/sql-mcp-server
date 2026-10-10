package configapp

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
)

// A configuration as workspace items is its Sections, item by item: each
// entity (keyed by its ID), each role and each user (by name), and the
// settings as one item.

// The kinds of workspace items, as the console names its changes.
const (
	ItemEntity   = "entity"
	ItemRole     = "role"
	ItemUser     = "user"
	ItemSettings = "settings"
)

// Schema is how the configuration's values merge, read off the item types
// (see mergeSchema): fields and relationships keyed by name, grants by the
// entity they name; optional text, and switches off unless set, unset when
// empty or false (an empty token hash: no token).
var Schema = mergeSchema(EntityItem{}, RoleItem{}, UserItem{})

// sectionsDoc is Sections with each item as it is stored.
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

// itemsOfSections are the items of encoded Sections, in order.
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

// sectionsOfItems are the encoded Sections of items.
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

// Items implements workspaceops.Codec.
func (c codec) Items(rev revision.Revision) (workspace.Snapshot, error) {
	if _, err := bootstrap.LoadRevision(rev); err != nil {
		return nil, err
	}
	return c.itemsOf(rev.Payload)
}

// itemsOf are the items of a payload, as it says.
func (c codec) itemsOf(payload []byte) (workspace.Snapshot, error) {
	sections, err := SectionsOfPayload(payload)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(sections)
	if err != nil {
		return nil, err
	}
	return itemsOfSections(data)
}

// edit is items as an edit of a base revision.
func (c codec) edit(items workspace.Snapshot) (configedit.Edit, error) {
	edit, err := c.decode(items)
	if err != nil {
		return configedit.Edit{}, fmt.Errorf("%w: %w", ErrWorkspaceContent, err)
	}
	return edit, nil
}

func (c codec) decode(items workspace.Snapshot) (configedit.Edit, error) {
	data, err := sectionsOfItems(items)
	if err != nil {
		return configedit.Edit{}, err
	}
	var sections Sections
	if err := json.Unmarshal(data, &sections); err != nil {
		return configedit.Edit{}, fmt.Errorf("workspace: %w", err)
	}
	return sections.Edit()
}

// ErrWorkspaceContent is a workspace whose items do not read as
// configuration (an agent wrote a section of the wrong shape): what it
// would configure cannot be told, as a validation of it reports.
var ErrWorkspaceContent = errors.New("workspace items are not a configuration")

// Build implements workspaceops.Codec: the configuration items make on
// base, checked as a whole.
func (c codec) Build(base revision.Revision, items workspace.Snapshot) ([]byte, error) {
	edit, err := c.edit(items)
	if err != nil {
		return nil, err
	}
	was, err := c.Items(base)
	if err != nil {
		return nil, err
	}
	removeSettings(was, &edit)
	_, payload, err := configedit.Apply(base.Payload, edit)
	return payload, err
}

// removeSettings makes the settings item the settings as they are: the edit
// overlays base (its items) key by key, so a key base has and the item does
// not is removed (null), its default applying.
func removeSettings(base workspace.Snapshot, edit *configedit.Edit) {
	settings, _ := base.Get(workspace.Ref{Kind: ItemSettings, Key: ItemSettings}).(map[string]any)
	for key := range settings {
		if _, kept := edit.Settings[key]; !kept {
			if edit.Settings == nil {
				edit.Settings = map[string]any{}
			}
			edit.Settings[key] = nil
		}
	}
}
