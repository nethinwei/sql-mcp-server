package bootstrap

import (
	"crypto/x509"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

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
	reg, err := entity.NewRegistry(entities)
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
		Roles:           make(map[string]map[string][]rbac.Grant, len(cfg.Roles)),
		RolePermissions: make(map[string][]string, len(cfg.Roles)),
		Principals:      make(map[string]rbac.Principal, len(cfg.Users)),
	}
	refs := config.NewEntityRefs(cfg.Entities)
	for name, def := range cfg.Roles {
		grants, err := compileGrants("role:"+name, def.Grants, refs)
		if err != nil {
			return rbac.Policy{}, fmt.Errorf("role %q: %w", name, err)
		}
		policy.Roles[name] = grants
		if len(def.Permissions) > 0 {
			policy.RolePermissions[name] = append([]string(nil), def.Permissions...)
		}
	}
	for name, user := range cfg.Users {
		if user.Disabled {
			continue
		}
		grants, err := compileGrants(UserPrincipal(name), user.Grants, refs)
		if err != nil {
			return rbac.Policy{}, fmt.Errorf("user %q: %w", name, err)
		}
		policy.Principals[UserPrincipal(name)] = rbac.Principal{
			Roles: append([]string(nil), user.Roles...), Grants: grants,
			Permissions: append([]string(nil), user.Permissions...),
		}
	}
	return policy, nil
}

// compileGrants compiles grants by the ID of the entity each grants.
func compileGrants(owner string, grants []config.GrantConfig, refs config.EntityRefs) (map[string][]rbac.Grant, error) {
	out := make(map[string][]rbac.Grant, len(grants))
	for i, gc := range grants {
		rows, err := config.ParseFilter(gc.Rows)
		if err != nil {
			return nil, fmt.Errorf("grant %d rows: %w", i, err)
		}
		g := rbac.Grant{ID: fmt.Sprintf("%s#%d", owner, i), Rows: rows}
		for _, name := range gc.Actions {
			action, ok := entity.ParseAction(name)
			if !ok {
				return nil, fmt.Errorf("grant %d: unknown action %q", i, name)
			}
			g.Actions = append(g.Actions, action)
		}
		if gc.Fields != nil {
			g.Fields = &entity.FieldPermissions{Read: gc.Fields.Read, Write: gc.Fields.Write}
		}
		id := entityID(refs, gc.Entity)
		out[id] = append(out[id], g)
	}
	return out, nil
}

// userBudgets returns each enabled user's effective limits keyed by principal:
// an explicit budget.users entry wins; otherwise every dimension takes the most
// permissive value among the user's roles (see roleBudgets), where zero
// (unlimited) wins and a role without a budget counts as unlimited. The
// fields the user's limits.budget sets win over either.
func userBudgets(cfg *config.Config) map[string]config.BudgetLimits {
	out := make(map[string]config.BudgetLimits, len(cfg.Users))
	roles := roleBudgets(cfg)
	for name, user := range cfg.Users {
		if user.Disabled {
			continue
		}
		limits, ok := cfg.Budget.Users[name]
		if !ok {
			limits = mostPermissiveBudget(roles, user.Roles)
		}
		out[UserPrincipal(name)] = overBudget(limits, user.Limits.Budget)
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
	for _, name := range names[1:] {
		limits, ok := roles[name]
		if !ok {
			return config.BudgetLimits{}
		}
		merged.MaxConcurrent = looser(merged.MaxConcurrent, limits.MaxConcurrent)
		merged.MaxExecution = looser(merged.MaxExecution, limits.MaxExecution)
		merged.MaxEstimatedScannedRows = looser(merged.MaxEstimatedScannedRows, limits.MaxEstimatedScannedRows)
		merged.MaxReturnedRows = looser(merged.MaxReturnedRows, limits.MaxReturnedRows)
		merged.MaxReturnedBytes = looser(merged.MaxReturnedBytes, limits.MaxReturnedBytes)
		merged.MaxSessionCost = looser(merged.MaxSessionCost, limits.MaxSessionCost)
	}
	return merged
}

// looser returns the more permissive of two limits, where zero is unlimited.
func looser[T ~int | ~int64 | ~float64](a, b T) T {
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

// userDirectory returns enabled users by name, by tokenHash and by
// certificate subject.
func userDirectory(cfg *config.Config) (users map[string]UserIdentity, tokens, certs map[string]string) {
	users = make(map[string]UserIdentity, len(cfg.Users))
	tokens = make(map[string]string, len(cfg.Users))
	certs = map[string]string{}
	for name, user := range cfg.Users {
		if user.Disabled {
			continue
		}
		users[name] = UserIdentity{Principal: UserPrincipal(name), Roles: user.Roles, Subject: user.Subject}
		if user.TokenHash != "" {
			tokens[user.TokenHash] = name
		}
		for _, s := range user.CertSubjects {
			certs[s] = name
		}
	}
	return users, tokens, certs
}

// CertSubjects are the users[].certSubjects forms a client certificate
// presents: its common name and its DNS, URI and email SANs.
func CertSubjects(leaf *x509.Certificate) []string {
	var out []string
	if leaf.Subject.CommonName != "" {
		out = append(out, config.CanonicalCertSubject("cn:"+leaf.Subject.CommonName))
	}
	for _, name := range leaf.DNSNames {
		out = append(out, config.CanonicalCertSubject("dns:"+name))
	}
	for _, uri := range leaf.URIs {
		out = append(out, config.CanonicalCertSubject("uri:"+uri.String()))
	}
	for _, email := range leaf.EmailAddresses {
		out = append(out, config.CanonicalCertSubject("email:"+email))
	}
	return out
}

// userByCert is the one user whose certSubjects the certificate matches; a
// certificate matching none, or several users, is no user.
func userByCert(certs map[string]string, leaf *x509.Certificate) (string, bool) {
	found := ""
	for _, s := range CertSubjects(leaf) {
		name, ok := certs[s]
		switch {
		case !ok:
		case found == "":
			found = name
		case found != name:
			return "", false
		}
	}
	return found, found != ""
}

// UserByCertificate returns the enabled user a client certificate names.
func (a *App) UserByCertificate(leaf *x509.Certificate) (UserIdentity, bool) {
	name, ok := userByCert(a.UserCerts, leaf)
	if !ok {
		return UserIdentity{}, false
	}
	return a.UserByName(name)
}

// UserByTokenHash returns the enabled user whose tokenHash matches.
func (a *App) UserByTokenHash(hash string) (UserIdentity, bool) {
	name, ok := a.UserTokens[hash]
	if !ok {
		return UserIdentity{}, false
	}
	return a.UserByName(name)
}

// UserByName returns an enabled user by name, none when its runtime holds
// the user.
func (a *App) UserByName(name string) (UserIdentity, bool) {
	id, ok := a.Users[name]
	if ok && a.held != nil && a.held(name) {
		return UserIdentity{}, false
	}
	return id, ok
}
