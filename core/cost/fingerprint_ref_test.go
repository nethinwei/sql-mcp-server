package cost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
)

// fingerprintReference is Fingerprint as first written: what it computes
// must not change, or every template captured stops matching.
func fingerprintReference(datasource, dialectName string, c codegen.Compiled) string {
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

type namedID int32

func TestFingerprintMatchesItsReference(t *testing.T) {
	t.Parallel()
	args := []any{nil, "x", int64(1), 1, 1.5, true, []byte("b"), time.Now(), namedID(3), int32(4), uint8(5),
		[]any{1}, map[string]any{}, struct{}{}, &struct{}{}}
	for _, sql := range []string{"", "  SELECT\t*\n FROM  t  ", "SELECT x FROM y", "a  b\r\nc"} {
		for n := range len(args) + 1 {
			c := codegen.Compiled{SQL: sql, Args: args[:n]}
			got, want := Fingerprint(" Shop ", "PostgreSQL", c), fingerprintReference(" Shop ", "PostgreSQL", c)
			if got != want {
				t.Fatalf("%q with %d args: %s, want %s", sql, n, got, want)
			}
		}
	}
}

func BenchmarkFingerprint(b *testing.B) {
	args := make([]any, 1000)
	for i := range args {
		args[i] = int64(i)
	}
	in := strings.Repeat("?, ", 999) + "?"
	c := codegen.Compiled{SQL: "SELECT id, name FROM orders WHERE id IN (" + in + ")", Args: args}
	b.ReportAllocs()
	for b.Loop() {
		_ = Fingerprint("shop", "postgres", c)
	}
}
