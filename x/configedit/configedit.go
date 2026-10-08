// Package configedit applies edits to a stored configuration: it replaces
// whole sections of a base payload with typed values and runs the result
// through the same chain as `store import` (strict decoding, defaults,
// validation, the store secret rule and the deterministic encoding). Callers
// such as the admin API map their DTOs to an Edit; the merge and the
// field-preservation rules live here.
package configedit

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// Edit replaces sections of a configuration. A nil section keeps the base.
type Edit struct {
	Entities []config.EntityConfig
	Roles    []Role
	Users    []User
	// Settings replaces top-level sections other than datasources,
	// entities, roles and users, as decoded JSON/YAML values.
	Settings map[string]any
}

// Role is a named role definition.
type Role struct {
	Name string
	config.RoleDefinition
}

// User is a named user. KeepToken keeps the base user's tokenHash, so a
// client never needs to hold token hashes to edit a user; an explicitly empty
// TokenHash without KeepToken revokes the token.
type User struct {
	Name string
	config.UserConfig
	KeepToken bool
}

// sections an Edit replaces through typed fields, never through Settings.
var structured = []string{"database", "databases", "datasources", "entities", "roles", "users"}

// Apply returns the configuration base with edit applied, and its payload.
// The result is validated as a whole, so an edit may add a user and a budget
// for it at once.
func Apply(base []byte, edit Edit) (*config.Config, []byte, error) {
	baseCfg, err := configyaml.Decode(base)
	if err != nil {
		return nil, nil, fmt.Errorf("base: %w", err)
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(base, &doc); err != nil {
		return nil, nil, fmt.Errorf("base: %w", err)
	}
	if err := applySettings(doc, edit.Settings); err != nil {
		return nil, nil, err
	}
	if err := applySections(doc, edit, baseCfg); err != nil {
		return nil, nil, err
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := configyaml.Decode(data)
	if err != nil {
		return nil, nil, err
	}
	payload, err := revisionops.Normalize(cfg)
	if err != nil {
		return nil, nil, err
	}
	return cfg, payload, nil
}

func applySettings(doc, settings map[string]any) error {
	for key, val := range settings {
		if slices.Contains(structured, key) {
			return fmt.Errorf("settings cannot change %q; datasources are managed by the CLI and "+
				"entities, roles and users have their own edit fields", key)
		}
		doc[key] = val
	}
	return nil
}

// applySections writes the typed sections into doc. Defaults are applied to
// them first so that unset values keep their meaning once encoded (an
// entity's unset mcp.dmlTools stays on).
func applySections(doc map[string]any, edit Edit, base *config.Config) error {
	sections := config.Config{Entities: edit.Entities}
	if edit.Roles != nil {
		sections.Roles = map[string]config.RoleDefinition{}
		for _, r := range edit.Roles {
			key := strings.ToLower(strings.TrimSpace(r.Name))
			if _, dup := sections.Roles[key]; dup {
				return fmt.Errorf("role %q listed twice", r.Name)
			}
			sections.Roles[key] = r.RoleDefinition
		}
	}
	if edit.Users != nil {
		sections.Users = map[string]config.UserConfig{}
		for _, u := range edit.Users {
			key := strings.ToLower(strings.TrimSpace(u.Name))
			if _, dup := sections.Users[key]; dup {
				return fmt.Errorf("user %q listed twice", u.Name)
			}
			if u.KeepToken {
				u.TokenHash = base.Users[key].TokenHash
			}
			sections.Users[key] = u.UserConfig
		}
	}
	sections.ApplyDefaults()
	for key, set := range map[string]bool{
		"entities": edit.Entities != nil, "roles": edit.Roles != nil, "users": edit.Users != nil,
	} {
		if !set {
			continue
		}
		val, err := plain(map[string]any{
			"entities": sections.Entities, "roles": sections.Roles, "users": sections.Users,
		}[key])
		if err != nil {
			return err
		}
		doc[key] = val
	}
	return nil
}

// plain converts typed values into generic YAML values, honoring their
// yaml tags (omitempty included).
func plain(v any) (any, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("configedit: empty section")
	}
	return out, nil
}
