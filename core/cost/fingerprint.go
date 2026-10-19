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
// so a v2 key of a statement with one no longer matches.
const fingerprintPrefix = "fp:v3:"

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
					"migrate it to fp:v3:<sha256> or prefix it with a datasource, for example %q",
				templates.name,
				value,
				names[0]+":"+value,
			)
		}
	}
	return nil
}

func validateFingerprintVersion(name string, values []string) error {
	for _, value := range values {
		if strings.HasPrefix(value, "fp:") && !strings.HasPrefix(value, fingerprintPrefix) {
			return fmt.Errorf("cost: %s entry %q is a fingerprint of an earlier version, which matches nothing; "+
				"capture it again as fp:v3:<sha256> (decimal arguments now bind as text)", name, value)
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
