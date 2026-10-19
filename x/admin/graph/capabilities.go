package graph

import (
	"sort"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

var graphPrivileges = map[introspect.Privilege]Privilege{
	introspect.PrivilegeGranted: PrivilegeGranted, introspect.PrivilegeDenied: PrivilegeDenied,
	introspect.PrivilegeUnknown: PrivilegeUnknown,
}

// capabilities returns the serving snapshot's capabilities, or nil.
func (r *Resolver) capabilities() bootstrap.EntityCapabilities {
	if r.App.Capabilities == nil {
		return nil
	}
	return r.App.Capabilities()
}

// toCapabilities flattens capabilities by entity and action, in a stable order.
func toCapabilities(caps bootstrap.EntityCapabilities) []EntityCapability {
	out := []EntityCapability{}
	for _, a := range entity.Actions {
		for name, actions := range caps {
			c, ok := actions[a]
			if !ok {
				continue
			}
			out = append(out, EntityCapability{
				Entity: name, Action: strings.ToUpper(a.String()), Privilege: graphPrivileges[c.Privilege],
				Connection: c.Connection, Columns: c.Columns, Reason: kit.Optional(c.Reason.Text),
				ReasonDetail: configapp.ReasonOf(c.Reason),
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

var (
	graphStrengths = map[dialect.Strength]CapabilityStrength{
		dialect.Enforced: CapabilityStrengthEnforced, dialect.BestEffort: CapabilityStrengthBestEffort,
		dialect.Unsupported: CapabilityStrengthUnsupported,
	}
	graphEvidence = map[dialect.Evidence]CapabilityEvidence{
		dialect.DBVerified: CapabilityEvidenceDbVerified, dialect.CoreVerified: CapabilityEvidenceCoreVerified,
		dialect.Declared: CapabilityEvidenceDeclared, dialect.External: CapabilityEvidenceExternal,
	}
)

// providerCapabilities is what the serving connections guarantee and the
// limits the serving configuration relies on that they do not enforce.
func (r *Resolver) providerCapabilities() *ProviderCapabilities {
	out := &ProviderCapabilities{Connections: []ConnectionCapabilities{}, Issues: []LimitIssue{}}
	if r.App.Profiles == nil {
		return out
	}
	profiles, issues := r.App.Profiles()
	for _, p := range profiles {
		c := ConnectionCapabilities{Datasource: p.Datasource, Connection: p.Connection, Claims: []CapabilityClaim{}}
		for _, k := range dialect.Keys {
			claim := p.Profile.Claim(k)
			c.Claims = append(c.Claims, CapabilityClaim{
				Key: string(k), Strength: graphStrengths[claim.Strength], Evidence: graphEvidence[claim.Evidence],
				Reason: kit.Optional(claim.Reason.Text), ReasonDetail: configapp.ReasonOf(claim.Reason),
			})
		}
		out.Connections = append(out.Connections, c)
	}
	for _, i := range issues {
		out.Issues = append(out.Issues, LimitIssue{
			Datasource: i.Datasource, Connection: i.Connection, Key: string(i.Key), Use: i.Use.Text,
			UseDetail: configapp.ReasonOf(i.Use), Strength: graphStrengths[i.Claim.Strength],
			Reason: kit.Optional(i.Claim.Reason.Text), ReasonDetail: configapp.ReasonOf(i.Claim.Reason),
		})
	}
	return out
}
