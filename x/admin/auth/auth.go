// Package auth holds what the admin HTTP layer and the GraphQL resolvers
// share: argon2id password hashing, admin permissions and the signed-in
// principal on a request context.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Admin permissions. PermAll grants every permission.
const (
	PermRead     = "admin:read"
	PermWrite    = "admin:write"
	PermPublish  = "admin:publish"
	PermAccounts = "admin:accounts"
	PermAll      = "admin:*"
)

var knownPermissions = []string{PermRead, PermWrite, PermPublish, PermAccounts, PermAll}

// Errors.
var (
	ErrForbidden     = errors.New("admin: permission denied")
	ErrUnauthorized  = errors.New("admin: not signed in")
	ErrWeakPassword  = errors.New("admin: password must be at least 12 characters")
	ErrBadPermission = errors.New("admin: unknown permission")
	ErrBadUsername   = errors.New("admin: username must match [a-z0-9][a-z0-9_.-]{0,63}")
)

// ValidatePermissions rejects unknown or duplicate permissions.
func ValidatePermissions(perms []string) error {
	seen := map[string]bool{}
	for _, p := range perms {
		if !slices.Contains(knownPermissions, p) {
			return fmt.Errorf("%w %q (known: %s)", ErrBadPermission, p, strings.Join(knownPermissions, ", "))
		}
		if seen[p] {
			return fmt.Errorf("%w: %q listed twice", ErrBadPermission, p)
		}
		seen[p] = true
	}
	return nil
}

// ValidateUsername checks the account name charset.
func ValidateUsername(name string) error {
	if name == "" || len(name) > 64 {
		return ErrBadUsername
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && (r == '_' || r == '.' || r == '-')
		if !ok {
			return ErrBadUsername
		}
	}
	return nil
}

// ValidatePassword enforces the minimum password length.
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < 12 {
		return ErrWeakPassword
	}
	return nil
}

// Principal is the signed-in administrator of a request.
type Principal struct {
	Username    string
	Permissions []string
}

// Has reports whether the principal holds perm (directly or via admin:*).
func (p Principal) Has(perm string) bool {
	return slices.Contains(p.Permissions, PermAll) || slices.Contains(p.Permissions, perm)
}

type principalKey struct{}

// WithPrincipal attaches the signed-in administrator to ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the signed-in administrator.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Require returns the principal when it holds perm.
func Require(ctx context.Context, perm string) (Principal, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return Principal{}, ErrUnauthorized
	}
	if !p.Has(perm) {
		return Principal{}, fmt.Errorf("%w: %s required", ErrForbidden, perm)
	}
	return p, nil
}

// argon2id parameters (RFC 9106 second recommended option).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword returns a PHC-encoded argon2id hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// dummyHash keeps VerifyPassword's cost the same for unknown accounts.
var dummyHash, _ = HashPassword("sql-mcp-server-dummy-password")

// VerifyPassword compares password with a PHC argon2id hash in constant
// time. An empty hash (unknown account) still spends one hash computation.
func VerifyPassword(hash, password string) bool {
	if hash == "" {
		hash = dummyHash
		password += "\x00mismatch"
	}
	var version, memory, iterations int
	var threads uint8
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(iterations), uint32(memory), threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
