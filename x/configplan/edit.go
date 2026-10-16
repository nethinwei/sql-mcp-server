package configplan

import (
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
)

// Edit is the proposal as an edit of the configuration it was evaluated
// against: the sections it changes, whole, as what they say (config.SpecOf:
// a default is left unset, as before it applied).
func (p Proposal) Edit() configedit.Edit {
	var edit configedit.Edit
	if len(p.Entities) > 0 || len(p.Removed) > 0 {
		edit.Entities = slices.Clone(config.SpecOf(&config.Config{Entities: p.Config.Entities}).Entities)
	}
	if len(p.Roles) > 0 {
		roles := config.SpecOf(&config.Config{Roles: p.Config.Roles}).Roles
		edit.Roles = []configedit.Role{}
		for _, name := range kit.SortedKeys(p.Config.Roles) {
			edit.Roles = append(edit.Roles, configedit.Role{Name: name, RoleDefinitionSpec: roles[name]})
		}
	}
	if len(p.Users) > 0 {
		users := config.SpecOf(&config.Config{Users: p.Config.Users}).Users
		edit.Users = []configedit.User{}
		for _, name := range kit.SortedKeys(p.Config.Users) {
			edit.Users = append(edit.Users, configedit.User{Name: name, UserSpec: users[name]})
		}
	}
	return edit
}
