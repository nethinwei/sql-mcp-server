package graph

import (
	"sort"

	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

var graphPrivileges = map[introspect.Privilege]Privilege{
	introspect.PrivilegeGranted: PrivilegeGranted, introspect.PrivilegeDenied: PrivilegeDenied,
	introspect.PrivilegeUnknown: PrivilegeUnknown,
}

// capabilities returns the serving snapshot's capabilities, or nil.
func (r *Resolver) capabilities() bootstrap.EntityCapabilities {
	if r.Capabilities == nil {
		return nil
	}
	return r.Capabilities()
}

// toCapabilities flattens capabilities by entity and action, in a stable order.
func toCapabilities(caps bootstrap.EntityCapabilities) []EntityCapability {
	out := []EntityCapability{}
	for action, a := range graphActions {
		for name, actions := range caps {
			c, ok := actions[a]
			if !ok {
				continue
			}
			out = append(out, EntityCapability{
				Entity: name, Action: action, Privilege: graphPrivileges[c.Privilege],
				Connection: c.Connection, Columns: c.Columns, Reason: optional(c.Reason),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Entity != out[j].Entity {
			return out[i].Entity < out[j].Entity
		}
		return out[i].Action < out[j].Action
	})
	return out
}
