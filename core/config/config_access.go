package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// UserPrincipalPrefix prefixes the principal key of a configured user. Role
// and user names cannot contain ':', so the prefix never collides with a role.
const UserPrincipalPrefix = "user:"

// StoreTablePrefix prefixes every configuration store table. Entities may
// not use such a source and introspection skips such tables, so the store is
// never exposed through the governed data plane.
const StoreTablePrefix = "smcp_"

// IsStoreTable reports whether a table name is reserved for the store.
func IsStoreTable(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), StoreTablePrefix)
}

// TokenHashPrefix is the only supported tokenHash algorithm.
const TokenHashPrefix = "sha256:"

// NewUserToken returns a 256-bit random bearer token ("smcp_" + base64url).
func NewUserToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "smcp_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// DefaultSecretRoots are the directories ${file:...} secrets may be read from
// when server.secrets.allowedRoots is unset.
func DefaultSecretRoots() []string { return []string{"/run/secrets", "/var/run/secrets"} }

// TokenHash returns the tokenHash form ("sha256:<hex>") of a bearer token.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return TokenHashPrefix + hex.EncodeToString(sum[:])
}

func (c *Config) normalizeAccess() error {
	if err := c.normalizeRoleDefinitions(); err != nil {
		return err
	}
	if err := c.normalizeUsers(); err != nil {
		return err
	}
	c.Server.User = canonicalRole(c.Server.User)
	budgetUsers := make(map[string]BudgetLimits, len(c.Budget.Users))
	for name, limits := range c.Budget.Users {
		key := canonicalRole(name)
		if _, exists := budgetUsers[key]; exists {
			return fmt.Errorf("config: budget has colliding user %q after normalization", key)
		}
		budgetUsers[key] = limits
	}
	if c.Budget.Users != nil {
		c.Budget.Users = budgetUsers
	}
	return nil
}

func (c *Config) normalizeRoleDefinitions() error {
	roles := make(map[string]RoleDefinition, len(c.Roles))
	for name, def := range c.Roles {
		key, err := canonicalAccessName("role", name)
		if err != nil {
			return err
		}
		if _, exists := roles[key]; exists {
			return fmt.Errorf("config: colliding role %q after normalization", key)
		}
		for i := range def.Grants {
			def.Grants[i].Actions = canonicalRoleList(def.Grants[i].Actions)
		}
		roles[key] = def
	}
	if c.Roles != nil {
		c.Roles = roles
	}
	return nil
}

func (c *Config) normalizeUsers() error {
	users := make(map[string]UserConfig, len(c.Users))
	for name, user := range c.Users {
		key, err := canonicalAccessName("user", name)
		if err != nil {
			return err
		}
		if _, exists := users[key]; exists {
			return fmt.Errorf("config: colliding user %q after normalization", key)
		}
		user.Roles = canonicalRoleList(user.Roles)
		user.TokenHash = strings.ToLower(strings.TrimSpace(user.TokenHash))
		for i := range user.Grants {
			user.Grants[i].Actions = canonicalRoleList(user.Grants[i].Actions)
		}
		users[key] = user
	}
	if c.Users != nil {
		c.Users = users
	}
	return nil
}

func canonicalAccessName(kind, name string) (string, error) {
	key := canonicalRole(name)
	if key == "" {
		return "", fmt.Errorf("config: empty %s name", kind)
	}
	if !patterns["accessName"].MatchString(key) {
		return "", fmt.Errorf("config: %s name %q must match %s", kind, key, patterns["accessName"])
	}
	return key, nil
}

// validateAccess checks top-level roles, users, tenant policies and user
// budgets. It runs after entity validation so entity references are known.
func (c *Config) validateAccess() error {
	entities := make(map[string]EntityConfig, len(c.Entities))
	for _, e := range c.Entities {
		entities[e.Name] = e
		if e.TenantPolicy != nil {
			if err := validateRowPolicy(e.TenantPolicy); err != nil {
				return fmt.Errorf("config: entity %q tenant policy: %w", e.Name, err)
			}
		}
	}
	for name, def := range c.Roles {
		if err := validatePermissions("role", name, def.Permissions); err != nil {
			return err
		}
		if err := validateGrants("role", name, def.Grants, entities); err != nil {
			return err
		}
	}
	return c.validateUsers(entities)
}

func (c *Config) validateUsers(entities map[string]EntityConfig) error {
	known := c.knownRoles()
	if len(c.Users) > 0 {
		for role := range known {
			if strings.Contains(role, ":") {
				return fmt.Errorf("config: role %q must not contain ':' when users are configured", role)
			}
		}
	}
	hashes := make(map[string]string, len(c.Users))
	if c.Server.Auth.Token != "" {
		hashes[TokenHash(c.Server.Auth.Token)] = "server.auth.token"
	}
	for name, user := range c.Users {
		if err := validateUser(name, user, known, hashes, entities); err != nil {
			return err
		}
	}
	if c.Server.User != "" {
		user, ok := c.Users[c.Server.User]
		if !ok {
			return fmt.Errorf("config: server.user references unknown user %q", c.Server.User)
		}
		if user.Disabled {
			return fmt.Errorf("config: server.user %q is disabled", c.Server.User)
		}
	}
	for name := range c.Budget.Users {
		if _, ok := c.Users[name]; !ok {
			return fmt.Errorf("config: budget references unknown user %q", name)
		}
	}
	return nil
}

// validateUser checks one user; hashes accumulates tokenHash owners so that
// collisions between users and with the shared token are rejected.
func validateUser(
	name string,
	user UserConfig,
	known map[string]bool,
	hashes map[string]string,
	entities map[string]EntityConfig,
) error {
	if user.TokenHash != "" {
		if other, exists := hashes[user.TokenHash]; exists {
			return fmt.Errorf("config: user %q tokenHash collides with %s", name, other)
		}
		hashes[user.TokenHash] = fmt.Sprintf("user %q", name)
	}
	if duplicate, ok := firstDuplicate(user.Roles); ok {
		return fmt.Errorf("config: user %q lists role %q twice", name, duplicate)
	}
	for _, role := range user.Roles {
		if !known[role] {
			return fmt.Errorf("config: user %q references unknown role %q", name, role)
		}
	}
	if err := validatePermissions("user", name, user.Permissions); err != nil {
		return err
	}
	return validateGrants("user", name, user.Grants, entities)
}

// knownRoles returns every role name defined at the top level or referenced
// by entity-level roles, fieldACL or rowPolicies.
func (c *Config) knownRoles() map[string]bool {
	known := make(map[string]bool, len(c.Roles))
	for name := range c.Roles {
		known[name] = true
	}
	for _, e := range c.Entities {
		for _, list := range [][]string{
			e.Roles.Read, e.Roles.Create, e.Roles.Update, e.Roles.Delete, e.Roles.Execute, e.Roles.Aggregate,
		} {
			for _, role := range list {
				known[role] = true
			}
		}
		for role := range e.FieldACL {
			known[role] = true
		}
		for role := range e.RowPolicies {
			known[role] = true
		}
	}
	return known
}

func validatePermissions(kind, owner string, permissions []string) error {
	if len(permissions) > 0 {
		return fmt.Errorf(
			"config: %s %q declares system permission %q, which is not supported in this version",
			kind, owner, permissions[0],
		)
	}
	return nil
}

func validateGrants(kind, owner string, grants []GrantConfig, entities map[string]EntityConfig) error {
	for i, grant := range grants {
		where := fmt.Sprintf("config: %s %q grant %d", kind, owner, i)
		e, ok := entities[grant.Entity]
		if !ok {
			return fmt.Errorf("%s references unknown entity %q", where, grant.Entity)
		}
		if duplicate, ok := firstDuplicate(grant.Actions); ok {
			return fmt.Errorf("%s on entity %q lists action %q twice", where, grant.Entity, duplicate)
		}
		if grant.Fields != nil {
			if err := validateFieldList(e, grant.Fields.Read, grant.Fields.Write); err != nil {
				return fmt.Errorf("%s on entity %q: %w", where, grant.Entity, err)
			}
		}
		if grant.Rows != nil {
			if err := validateRowPolicy(grant.Rows); err != nil {
				return fmt.Errorf("%s on entity %q rows: %w", where, grant.Entity, err)
			}
		}
	}
	return nil
}

func validateFieldList(e EntityConfig, read, write []string) error {
	visible := visibleFieldNames(e.Fields)
	if duplicate, ok := firstDuplicate(read); ok {
		return fmt.Errorf("duplicate read field %q", duplicate)
	}
	if duplicate, ok := firstDuplicate(write); ok {
		return fmt.Errorf("duplicate write field %q", duplicate)
	}
	for _, field := range append(append([]string{}, read...), write...) {
		if !visible[field] {
			return fmt.Errorf("unknown or excluded field %q", field)
		}
	}
	return nil
}
