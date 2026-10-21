package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// CanonicalCertSubject is a users[].certSubjects entry as matched: the kind
// in lower case, and DNS names and email addresses in lower case too (they
// are case-insensitive; common names and URIs are compared as written).
func CanonicalCertSubject(s string) string {
	kind, value, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return s
	}
	kind = strings.ToLower(kind)
	if kind == "dns" || kind == "email" {
		value = strings.ToLower(value)
	}
	return kind + ":" + value
}

// validateCertSubjects requires each certificate subject to name one user,
// and no proxy: a certificate that is both a proxy's and a user's would let
// that user speak for others through the identity headers. Only cn: and dns:
// subjects can be compared with a proxy's names (a CN or DNS SAN); which
// names a certificate of a uri: or email: subject also carries is the CA's
// choice, so the server never trusts a certificate naming a user as a proxy's
// (mcpserver.fromProxyCertificate).
func (c *Config) validateCertSubjects() error {
	owner := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(c.Users)) {
		for _, s := range c.Users[name].CertSubjects {
			if other, ok := owner[s]; ok {
				return fmt.Errorf("config: certificate subject %q names users %q and %q", s, other, name)
			}
			owner[s] = name
			kind, value, _ := strings.Cut(s, ":")
			if (kind == "cn" || kind == "dns") &&
				slices.ContainsFunc(c.Server.Auth.TrustedProxyCertSubjects, func(p string) bool {
					return strings.EqualFold(p, value)
				}) {
				return fmt.Errorf("config: user %q certificate subject %q is a trusted proxy's", name, s)
			}
		}
	}
	return nil
}
