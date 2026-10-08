package bootstrap

import (
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

var configActions = map[string]entity.Action{
	"read": entity.ActionRead, "create": entity.ActionCreate, "update": entity.ActionUpdate,
	"delete": entity.ActionDelete, "execute": entity.ActionExecute, "aggregate": entity.ActionAggregate,
}

// UserPrincipal returns the principal key of a configured user.
func UserPrincipal(user string) string {
	return config.UserPrincipalPrefix + user
}

// OfflineAuthorizer compiles cfg's entities, roles and users into an
// authorizer without connecting to any database, for policy simulation.
func OfflineAuthorizer(cfg *config.Config) (rbac.Authorizer, error) {
	entities, err := configToEntities(cfg.Entities)
	if err != nil {
		return nil, err
	}
	reg, err := entityRegistryFromConfig(entities)
	if err != nil {
		return nil, err
	}
	policy, err := accessPolicy(cfg)
	if err != nil {
		return nil, err
	}
	return rbac.NewGrantAuthorizer(reg, policy), nil
}

// accessPolicy compiles top-level roles and users into an rbac.Policy.
// Entity-level role configuration is read by the authorizer from the registry.
func accessPolicy(cfg *config.Config) (rbac.Policy, error) {
	policy := rbac.Policy{
		Roles:      make(map[string]map[string][]rbac.Grant, len(cfg.Roles)),
		Principals: make(map[string]rbac.Principal, len(cfg.Users)),
	}
	for name, def := range cfg.Roles {
		grants, err := compileGrants("role:"+name, def.Grants)
		if err != nil {
			return rbac.Policy{}, fmt.Errorf("role %q: %w", name, err)
		}
		policy.Roles[name] = grants
	}
	for name, user := range cfg.Users {
		if user.Disabled {
			continue
		}
		grants, err := compileGrants(UserPrincipal(name), user.Grants)
		if err != nil {
			return rbac.Policy{}, fmt.Errorf("user %q: %w", name, err)
		}
		policy.Principals[UserPrincipal(name)] = rbac.Principal{
			Roles: append([]string(nil), user.Roles...), Grants: grants,
		}
	}
	return policy, nil
}

func compileGrants(owner string, grants []config.GrantConfig) (map[string][]rbac.Grant, error) {
	out := make(map[string][]rbac.Grant, len(grants))
	for i, gc := range grants {
		rows, err := filterConfigToPredicate(gc.Rows)
		if err != nil {
			return nil, fmt.Errorf("grant %d rows: %w", i, err)
		}
		g := rbac.Grant{ID: fmt.Sprintf("%s#%d", owner, i), Rows: rows}
		for _, name := range gc.Actions {
			action, ok := configActions[name]
			if !ok {
				return nil, fmt.Errorf("grant %d: unknown action %q", i, name)
			}
			g.Actions = append(g.Actions, action)
		}
		if gc.Fields != nil {
			g.Fields = &entity.FieldPermissions{Read: gc.Fields.Read, Write: gc.Fields.Write}
		}
		out[gc.Entity] = append(out[gc.Entity], g)
	}
	return out, nil
}

// userBudgets returns each enabled user's effective limits keyed by principal:
// an explicit budget.users entry wins; otherwise every dimension takes the most
// permissive value among the user's roles, where zero (unlimited) wins and a
// role without a budget entry counts as unlimited.
func userBudgets(cfg *config.Config) map[string]config.BudgetLimits {
	out := make(map[string]config.BudgetLimits, len(cfg.Users))
	for name, user := range cfg.Users {
		if user.Disabled {
			continue
		}
		if limits, ok := cfg.Budget.Users[name]; ok {
			out[UserPrincipal(name)] = limits
			continue
		}
		out[UserPrincipal(name)] = mostPermissiveBudget(cfg.Budget.Roles, user.Roles)
	}
	return out
}

func mostPermissiveBudget(roles map[string]config.BudgetLimits, names []string) config.BudgetLimits {
	if len(names) == 0 {
		return config.BudgetLimits{}
	}
	merged, ok := roles[names[0]]
	if !ok {
		return config.BudgetLimits{}
	}
	merged.MaxEstimatedScannedRows = toBudgetLimits(merged).MaxEstimatedScannedRows
	merged.MaxScannedRows = 0
	for _, name := range names[1:] {
		limits, ok := roles[name]
		if !ok {
			return config.BudgetLimits{}
		}
		normalized := toBudgetLimits(limits)
		merged.MaxConcurrent = looser(merged.MaxConcurrent, limits.MaxConcurrent)
		merged.MaxExecution = looser(merged.MaxExecution, limits.MaxExecution)
		merged.MaxEstimatedScannedRows = looser(merged.MaxEstimatedScannedRows, normalized.MaxEstimatedScannedRows)
		merged.MaxReturnedRows = looser(merged.MaxReturnedRows, limits.MaxReturnedRows)
		merged.MaxReturnedBytes = looser(merged.MaxReturnedBytes, limits.MaxReturnedBytes)
		merged.MaxSessionCost = looser(merged.MaxSessionCost, limits.MaxSessionCost)
	}
	return merged
}

// looser returns the more permissive of two limits, where zero is unlimited.
func looser[T ~int | ~int64](a, b T) T {
	if a == 0 || b == 0 {
		return 0
	}
	return max(a, b)
}

// UserIdentity is the runtime view of an enabled user.
type UserIdentity struct {
	Principal string
	Roles     []string
	Subject   map[string]any
}

// userDirectory returns enabled users by name and by tokenHash.
func userDirectory(cfg *config.Config) (map[string]UserIdentity, map[string]string) {
	users := make(map[string]UserIdentity, len(cfg.Users))
	tokens := make(map[string]string, len(cfg.Users))
	for name, user := range cfg.Users {
		if user.Disabled {
			continue
		}
		users[name] = UserIdentity{Principal: UserPrincipal(name), Roles: user.Roles, Subject: user.Subject}
		if user.TokenHash != "" {
			tokens[user.TokenHash] = name
		}
	}
	return users, tokens
}

// UserByTokenHash returns the enabled user whose tokenHash matches.
func (a *App) UserByTokenHash(hash string) (UserIdentity, bool) {
	name, ok := a.UserTokens[hash]
	if !ok {
		return UserIdentity{}, false
	}
	return a.UserByName(name)
}

// UserByName returns an enabled user by name.
func (a *App) UserByName(name string) (UserIdentity, bool) {
	id, ok := a.Users[name]
	return id, ok
}
