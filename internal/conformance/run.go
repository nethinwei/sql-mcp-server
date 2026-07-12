package conformance

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/relalg/interp"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

// Setup creates and seeds the fixture table. Statements run one by one:
// MySQL-family drivers reject multi-statement strings.
func Setup(ctx context.Context, db store.DB, d dialect.Dialect, schema string) error {
	for _, stmt := range SetupStatements(d.Name(), schema) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("conformance setup %q: %w", stmt, err)
		}
	}
	return nil
}

// Run executes the whole corpus differentially: interpreter result vs the
// provider executing codegen output. Any mismatch is a conformance failure;
// per the spec it must be fixed or promoted to a documented deviation (and
// the corpus adjusted) — never silently accepted.
func Run(t *testing.T, db store.DB, d dialect.Dialect, schema string) {
	t.Helper()
	fixture := Fixture()
	for _, c := range Cases(schema) {
		t.Run(c.Name, func(t *testing.T) {
			want, err := interp.Eval(fixture, c.Expr)
			if err != nil {
				t.Fatalf("interpreter: %v", err)
			}
			compiled, err := codegen.Renderer{Dialect: d}.Compile(c.Expr)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got, err := queryRows(db, compiled.SQL, compiled.Args)
			if err != nil {
				t.Fatalf("execute %s: %v", compiled.SQL, err)
			}
			if err := compareRows(want.Rows, got, c.Ordered); err != nil {
				t.Fatalf("divergence on %s\nsql: %s\nargs: %v\n%v",
					c.Name, compiled.SQL, compiled.Args, err)
			}
		})
	}
}

// queryRows executes SQL and decodes rows positionally. Positional decoding
// is deliberate: aggregate column names are not part of the cross-provider
// contract (PG says "count", MySQL says "count(*)").
func queryRows(db store.DB, sql string, args []any) ([][]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, vals)
	}
	return out, rows.Err()
}

func compareRows(want, got [][]any, ordered bool) error {
	wantKeys, err := rowKeys(want)
	if err != nil {
		return fmt.Errorf("normalize interpreter rows: %w", err)
	}
	gotKeys, err := rowKeys(got)
	if err != nil {
		return fmt.Errorf("normalize provider rows: %w", err)
	}
	if !ordered {
		sort.Strings(wantKeys)
		sort.Strings(gotKeys)
	}
	if len(wantKeys) != len(gotKeys) {
		return fmt.Errorf("row count: interpreter %d, provider %d\ninterpreter: %v\nprovider: %v",
			len(wantKeys), len(gotKeys), wantKeys, gotKeys)
	}
	for i := range wantKeys {
		if wantKeys[i] != gotKeys[i] {
			return fmt.Errorf("row %d: interpreter %q, provider %q", i, wantKeys[i], gotKeys[i])
		}
	}
	return nil
}

func rowKeys(rows [][]any) ([]string, error) {
	keys := make([]string, len(rows))
	for i, row := range rows {
		parts := make([]string, len(row))
		for j, v := range row {
			k, err := canonicalKey(v)
			if err != nil {
				return nil, fmt.Errorf("row %d col %d: %w", i, j, err)
			}
			parts[j] = k
		}
		keys[i] = strings.Join(parts, "|")
	}
	return keys, nil
}

// decimalString matches driver-returned exact decimals ("4.1000", "-5").
// Fixture strings are alphabetic, so a numeric-looking string is always a
// numeric column coming back as text/[]byte (MySQL decimals, PG numerics).
var decimalString = regexp.MustCompile(`\A-?[0-9]+(\.[0-9]+)?\z`)

// canonicalKey normalizes a value from either side (interpreter or driver)
// into a canonical comparison key: NULL, exact rational, or string.
func canonicalKey(v any) (string, error) {
	if v == nil {
		return "<null>", nil
	}
	if r, ok := numericRat(v); ok {
		return "num:" + r.RatString(), nil
	}
	s, ok := stringValue(v)
	if !ok {
		return "", fmt.Errorf("unsupported value type %T (%v)", v, v)
	}
	if decimalString.MatchString(s) {
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			return "", fmt.Errorf("unparseable decimal %q", s)
		}
		return "num:" + r.RatString(), nil
	}
	return "str:" + s, nil
}

func numericRat(v any) (*big.Rat, bool) {
	switch n := v.(type) {
	case int64:
		return new(big.Rat).SetInt64(n), true
	case int32:
		return new(big.Rat).SetInt64(int64(n)), true
	case int:
		return new(big.Rat).SetInt64(int64(n)), true
	case uint64:
		return new(big.Rat).SetUint64(n), true
	case float64:
		return new(big.Rat).SetFloat64(n), true
	case *big.Rat:
		return n, true
	}
	return nil, false
}

func stringValue(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case []byte:
		return string(s), true
	}
	return "", false
}
