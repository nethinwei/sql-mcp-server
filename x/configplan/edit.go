package configplan

import (
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/x/configedit"
)

// Edit is the proposal as an edit of the configuration it was evaluated
// against: the sections it changes, whole.
func (p Proposal) Edit() configedit.Edit {
	var edit configedit.Edit
	if len(p.Entities) > 0 {
		edit.Entities = slices.Clone(p.Config.Entities)
	}
	if len(p.Roles) > 0 {
		edit.Roles = []configedit.Role{}
		for _, name := range sortedKeys(p.Config.Roles) {
			edit.Roles = append(edit.Roles, configedit.Role{Name: name, RoleDefinition: p.Config.Roles[name]})
		}
	}
	if len(p.Users) > 0 {
		edit.Users = []configedit.User{}
		for _, name := range sortedKeys(p.Config.Users) {
			edit.Users = append(edit.Users, configedit.User{Name: name, UserConfig: p.Config.Users[name]})
		}
	}
	return edit
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.SortFunc(out, strings.Compare)
	return out
}
