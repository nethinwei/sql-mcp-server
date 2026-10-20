package configapp

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
)

// A configuration as workspace items is its Sections, item by item: each
// entity (keyed by its ID), each role and each user (by name), and the
// settings as one item. An item's value is its JSON decoded (what the
// workspace stores and merges); its typed form is the item type of Sections.

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

// settingsRef names the one settings item.
var settingsRef = workspace.Ref{Kind: ItemSettings, Key: ItemSettings}

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

// jsonValue is v as JSON decodes it: the form items hold.
func jsonValue(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(raw, &out)
	return out, err
}

// itemsOfSections are the items of s, in order: entities, roles, users, then
// the settings.
func itemsOfSections(s Sections) (workspace.Snapshot, error) {
	out := make(workspace.Snapshot, 0, len(s.Entities)+len(s.Roles)+len(s.Users)+1)
	add := func(kind, key string, item any) error {
		v, err := jsonValue(item)
		if err != nil {
			return err
		}
		out = append(out, workspace.Item{Ref: workspace.Ref{Kind: kind, Key: key}, Value: v})
		return nil
	}
	for _, e := range s.Entities {
		if err := add(ItemEntity, entity.ID(kit.Deref(e.Datasource), kit.Deref(e.Schema), e.Name), e); err != nil {
			return nil, err
		}
	}
	for _, r := range s.Roles {
		if err := add(ItemRole, r.Name, r); err != nil {
			return nil, err
		}
	}
	for _, u := range s.Users {
		if err := add(ItemUser, u.Name, u); err != nil {
			return nil, err
		}
	}
	settings := s.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	return out, add(ItemSettings, ItemSettings, settings)
}

// rawSections are the items as the JSON of Sections, each item as it is
// kept: what the console reads, whether or not every item has the shape of
// its kind (one an agent wrote may not; see ErrWorkspaceContent).
func rawSections(items workspace.Snapshot) (json.RawMessage, error) {
	lists := map[string]string{ItemEntity: "entities", ItemRole: "roles", ItemUser: "users"}
	doc := map[string]any{"entities": []any{}, "roles": []any{}, "users": []any{}}
	var settings any = map[string]any{}
	for _, it := range items {
		if it.Kind == ItemSettings {
			settings = it.Value
			continue
		}
		if name, ok := lists[it.Kind]; ok {
			doc[name] = append(doc[name].([]any), it.Value)
		}
	}
	doc[ItemSettings] = settings
	return json.Marshal(doc)
}

// sectionsOfItems are the items as Sections: each item's value read as its
// kind's type (strictly; see ErrWorkspaceContent).
func sectionsOfItems(items workspace.Snapshot) (Sections, error) {
	s := Sections{Entities: []EntityItem{}, Roles: []RoleItem{}, Users: []UserItem{}, Settings: map[string]any{}}
	for _, it := range items {
		var err error
		switch it.Kind {
		case ItemEntity:
			var e EntityItem
			if e, err = decodeJSON[EntityItem](it.Value); err == nil {
				s.Entities = append(s.Entities, e)
			}
		case ItemRole:
			var r RoleItem
			if r, err = decodeJSON[RoleItem](it.Value); err == nil {
				s.Roles = append(s.Roles, r)
			}
		case ItemUser:
			var u UserItem
			if u, err = decodeJSON[UserItem](it.Value); err == nil {
				s.Users = append(s.Users, u)
			}
		case ItemSettings:
			if it.Value != nil {
				s.Settings, err = decodeJSON[map[string]any](it.Value)
			}
		}
		if err != nil {
			return Sections{}, fmt.Errorf("workspace %s %q: %w", it.Kind, it.Key, err)
		}
	}
	return s, nil
}

// codec is the configuration's workspaceops.Codec.
type codec struct{ r *Service }

// Items implements workspaceops.Codec.
func (c codec) Items(rev revision.Revision) (workspace.Snapshot, error) {
	spec, _, err := bootstrap.LoadRevisionSpec(rev)
	if err != nil {
		return nil, err
	}
	return itemsOfSpec(spec)
}

// itemsOfSpec are the items of what a configuration says.
func itemsOfSpec(spec *config.Spec) (workspace.Snapshot, error) {
	sections, err := SectionsOf(spec)
	if err != nil {
		return nil, err
	}
	return itemsOfSections(sections)
}

// itemsOfPayload are the items of a payload, as it says.
func itemsOfPayload(payload []byte) (workspace.Snapshot, error) {
	spec, err := configyaml.DecodeSpec(payload)
	if err != nil {
		return nil, err
	}
	return itemsOfSpec(spec)
}

// edit is items as an edit of a base revision.
func (c codec) edit(items workspace.Snapshot) (configedit.Edit, error) {
	sections, err := sectionsOfItems(items)
	if err != nil {
		return configedit.Edit{}, fmt.Errorf("%w: %w", ErrWorkspaceContent, err)
	}
	edit, err := sections.Edit()
	if err != nil {
		return configedit.Edit{}, fmt.Errorf("%w: %w", ErrWorkspaceContent, err)
	}
	return edit, nil
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
	spec, _, err := bootstrap.LoadRevisionSpec(base)
	if err != nil {
		return nil, err
	}
	removeSettings(spec, &edit)
	_, _, payload, err := configedit.ApplySpec(spec, edit)
	return payload, err
}

// removeSettings makes the settings item the settings as they are: the edit
// overlays base key by key, so a section base has and the item does not is
// removed (null), its default applying.
func removeSettings(base *config.Spec, edit *configedit.Edit) {
	removeSettingsBut(settingsKeys(base), edit)
}

// removeSettingsBut removes (null) the sections of keys, those base has,
// that edit does not set. The settings item of a configuration has the
// keys settingsKeys finds in it (TestSettingsKeysAreTheItemKeys).
func removeSettingsBut(keys []string, edit *configedit.Edit) {
	for _, key := range keys {
		if _, kept := edit.Settings[key]; !kept {
			if edit.Settings == nil {
				edit.Settings = map[string]any{}
			}
			edit.Settings[key] = nil
		}
	}
}
