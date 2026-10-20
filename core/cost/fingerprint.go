package cost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
)

// fingerprintPrefix versions Fingerprint: what it hashes changed in v3, when
// a decimal argument began binding as its text (a string) for its precision,
// and in v4, when reads began carrying their row cap as their LIMIT and
// PostgreSQL ordering NULLs last with NULLS LAST, so a key of an earlier
// version may no longer match.
const fingerprintPrefix = "fp:v4:"

// Fingerprint returns a stable baseline key derived from datasource, dialect,
// normalized generated SQL, and bound parameter types. Values are deliberately
// excluded.
func Fingerprint(datasource, dialectName string, c codegen.Compiled) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(strings.TrimSpace(datasource)))
	b.WriteByte('\n')
	b.WriteString(strings.ToLower(strings.TrimSpace(dialectName)))
	b.WriteByte('\n')
	b.WriteString(strings.Join(strings.Fields(c.SQL), " "))
	for _, arg := range c.Args {
		b.WriteByte('\n')
		if arg == nil {
			b.WriteString("<nil>")
			continue
		}
		t := reflect.TypeOf(arg)
		_, _ = fmt.Fprintf(&b, "%s/%s", t.PkgPath(), t.String())
	}
	sum := sha256.Sum256([]byte(b.String()))
	return fingerprintPrefix + hex.EncodeToString(sum[:])
}

// ValidateTemplateScopes rejects fingerprints of an earlier version, which
// match nothing (a reject that matches nothing lets its statement through),
// and legacy exact-SQL baselines that would stop matching when more than one
// datasource is configured.
func ValidateTemplateScopes(datasources, allowTemplates, rejectTemplates []string) error {
	if err := validateFingerprintVersion("allowTemplates", allowTemplates); err != nil {
		return err
	}
	if err := validateFingerprintVersion("rejectTemplates", rejectTemplates); err != nil {
		return err
	}
	if len(datasources) <= 1 {
		return nil
	}
	names := append([]string(nil), datasources...)
	sort.Strings(names)
	for _, templates := range []struct {
		name   string
		values []string
	}{
		{name: "allowTemplates", values: allowTemplates},
		{name: "rejectTemplates", values: rejectTemplates},
	} {
		for _, value := range templates.values {
			if strings.HasPrefix(value, fingerprintPrefix) || hasDatasourceScope(value, names) {
				continue
			}
			return fmt.Errorf(
				"cost: %s entry %q is legacy bare SQL and is unsafe with multiple datasources; "+
					"migrate it to %s<sha256> or prefix it with a datasource, for example %q",
				templates.name,
				value,
				fingerprintPrefix,
				names[0]+":"+value,
			)
		}
	}
	return nil
}

// validateFingerprintVersion refuses an entry naming a fingerprint that
// matches nothing: of an earlier version, or not written bare (a fingerprint
// is compared whole, and already names its datasource).
func validateFingerprintVersion(name string, values []string) error {
	for _, value := range values {
		fp := strings.TrimSpace(value)
		if _, scoped, ok := strings.Cut(fp, ":"); ok && !strings.HasPrefix(fp, "fp:") &&
			strings.HasPrefix(strings.TrimSpace(scoped), "fp:") {
			fp = strings.TrimSpace(scoped)
		}
		switch {
		case !strings.HasPrefix(fp, "fp:"):
		case fp != value:
			return fmt.Errorf("cost: %s entry %q matches nothing: write the fingerprint bare, as %q "+
				"(it names its datasource)", name, value, fp)
		case !strings.HasPrefix(fp, fingerprintPrefix):
			return fmt.Errorf("cost: %s entry %q is a fingerprint of an earlier version, which matches nothing; "+
				"capture it again as %s<sha256> (reads now carry their row cap as LIMIT)", name, value, fingerprintPrefix)
		}
	}
	return nil
}

func hasDatasourceScope(value string, datasources []string) bool {
	for _, datasource := range datasources {
		prefix := strings.TrimSpace(datasource) + ":"
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}

func matchesBaseline(
	values []string,
	datasource, dialectName string,
	c codegen.Compiled,
	allowLegacyExactSQL bool,
) bool {
	fp := Fingerprint(datasource, dialectName, c)
	scopedSQL := strings.TrimSpace(datasource) + ":" + c.SQL
	for _, value := range values {
		if value == fp || value == scopedSQL || allowLegacyExactSQL && value == c.SQL {
			return true
		}
	}
	return false
}
