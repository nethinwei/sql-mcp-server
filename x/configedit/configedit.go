// Package configedit applies edits to a stored configuration: it replaces
// whole sections of a base configuration with typed values and runs the
// result through the same chain as `store import` (defaults, validation, the
// store secret rule and the deterministic encoding). Callers such as the
// admin API map their DTOs to an Edit; the merge and the field-preservation
// rules live here.
package configedit

import (
	"fmt"
	"reflect"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

// Edit replaces sections of a configuration with what they say (Specs: an
// unset value stays unset, and takes its default when the configuration
// loads). A nil section keeps the base.
type Edit struct {
	Entities []config.EntitySpec
	Roles    []Role
	Users    []User
	// Settings replaces top-level sections other than datasources,
	// entities, roles and users, as decoded JSON/YAML values; a nil value
	// removes the section (its defaults apply).
	Settings map[string]any
}

// Role is a named role definition.
type Role struct {
	Name string
	config.RoleDefinitionSpec
}

// User is a named user, its token included: one without a token hash has
// none.
type User struct {
	Name string
	config.UserSpec
}

// sections an Edit replaces through typed fields, never through Settings.
var structured = []string{"database", "databases", "datasources", "entities", "roles", "users"}

// Apply is ApplySpec on a stored payload, which must load.
func Apply(base []byte, edit Edit) (*config.Config, []byte, error) {
	spec, err := configyaml.DecodeSpec(base)
	if err != nil {
		return nil, nil, fmt.Errorf("base: %w", err)
	}
	if _, err := config.Resolve(spec); err != nil {
		return nil, nil, fmt.Errorf("base: %w", err)
	}
	_, cfg, payload, err := ApplySpec(spec, edit)
	return cfg, payload, err
}

// ApplySpec returns the configuration base with edit applied: what it says,
// what it resolves to, and its payload. The result is validated as a whole,
// so an edit may add a user and a budget for it at once. base is left as it
// is, and may be shared by edits applied at once, when it is canonical, as a
// decoded payload is: making the result canonical then writes nothing of it.
func ApplySpec(base *config.Spec, edit Edit) (*config.Spec, *config.Config, []byte, error) {
	spec := *base
	if err := applySettings(&spec, edit.Settings); err != nil {
		return nil, nil, nil, err
	}
	if err := applySections(&spec, edit); err != nil {
		return nil, nil, nil, err
	}
	payload, cfg, err := revisionops.NormalizeResolved(&spec)
	if err != nil {
		return nil, nil, nil, err
	}
	return &spec, cfg, payload, nil
}

// applySettings replaces each top-level section settings names: its value
// is decoded as that section of a configuration (strictly: an unknown key
// fails), and nil removes it.
func applySettings(spec *config.Spec, settings map[string]any) error {
	if len(settings) == 0 {
		return nil
	}
	doc := make(map[string]any, len(settings))
	for key, val := range settings {
		if slices.Contains(structured, key) {
			return fmt.Errorf("settings cannot change %q; datasources are managed by the CLI and "+
				"entities, roles and users have their own edit fields", key)
		}
		if val != nil {
			doc[key] = val
		}
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	decoded, err := configyaml.DecodeSpec(data)
	if err != nil {
		return err
	}
	out, from := reflect.ValueOf(spec).Elem(), reflect.ValueOf(decoded).Elem()
	t := out.Type()
	for i := range t.NumField() {
		if _, named := settings[config.YAMLName(t.Field(i))]; named {
			out.Field(i).Set(from.Field(i))
		}
	}
	return nil
}

// applySections writes the typed sections into spec, as they say: an unset
// value is left out, so it keeps its default (an entity's unset
// mcp.dmlTools stays on).
func applySections(spec *config.Spec, edit Edit) error {
	if edit.Entities != nil {
		spec.Entities = slices.Clone(edit.Entities)
	}
	if edit.Roles != nil {
		roles := config.Map[string, config.RoleDefinitionSpec]{}
		for _, r := range edit.Roles {
			key := rbac.NormalizeRole(r.Name)
			if _, dup := roles[key]; dup {
				return fmt.Errorf("role %q listed twice", r.Name)
			}
			roles[key] = r.RoleDefinitionSpec
		}
		spec.Roles = roles
	}
	if edit.Users != nil {
		users := config.Map[string, config.UserSpec]{}
		for _, u := range edit.Users {
			key := rbac.NormalizeRole(u.Name)
			if _, dup := users[key]; dup {
				return fmt.Errorf("user %q listed twice", u.Name)
			}
			users[key] = u.UserSpec
		}
		spec.Users = users
	}
	return nil
}
