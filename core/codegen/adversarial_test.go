package codegen

import (
	"reflect"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/internal/testdialect"
)

func TestAdversarialValueIdentifierIsolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dialect dialect.Dialect
		table   string
		field   string
		value   string
		wantSQL string
	}{
		{
			name: "postgres quotes embedded delimiters", dialect: testdialect.Postgres{},
			table: `users"; DROP TABLE audit;--`, field: `na"me`, value: `' OR true;--`,
			wantSQL: `SELECT * FROM "users""; DROP TABLE audit;--" WHERE "na""me" = $1`,
		},
		{
			name: "mysql quotes embedded delimiters", dialect: testdialect.MySQL{},
			table: "users`; DROP TABLE audit;--", field: "na`me", value: `1 OR 1=1`,
			wantSQL: "SELECT * FROM `users``; DROP TABLE audit;--` WHERE `na``me` = ?",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiled, err := NewRenderer(test.dialect).Compile(relalg.Select{
				Input: relalg.Scan{Relation: relalg.RelationRef{Name: test.table}},
				Predicate: relalg.Condition{
					Field: test.field, Op: relalg.OpEq, Value: test.value,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if compiled.SQL != test.wantSQL {
				t.Fatalf("SQL = %q, want %q", compiled.SQL, test.wantSQL)
			}
			if !reflect.DeepEqual(compiled.Args, []any{test.value}) {
				t.Fatalf("args = %#v", compiled.Args)
			}
		})
	}
}

func FuzzCompileNoInjectionAndQuoting(f *testing.F) {
	f.Add(uint8(0), `users"; DROP TABLE audit;--`, `na"me`, `' OR true;--`)
	f.Add(uint8(1), "users`; DROP TABLE audit;--", "na`me", "1 OR 1=1")
	f.Add(uint8(0), "schema.table", "select", "\x00\n\r")

	f.Fuzz(func(t *testing.T, dialectID uint8, table, field, value string) {
		var d dialect.Dialect = testdialect.Postgres{}
		placeholder := "$1"
		if dialectID%2 == 1 {
			d = testdialect.MySQL{}
			placeholder = "?"
		}
		compiled, err := NewRenderer(d).Compile(relalg.Select{
			Input: relalg.Scan{Relation: relalg.RelationRef{Name: table}},
			Predicate: relalg.Condition{
				Field: field, Op: relalg.OpEq, Value: value,
			},
		})
		if field == "" {
			if err == nil {
				t.Fatal("empty field must fail closed")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		want := "SELECT * FROM " + d.QuoteIdent(table) + " WHERE " + d.QuoteIdent(field) + " = " + placeholder
		if compiled.SQL != want {
			t.Fatalf("SQL = %q, want %q", compiled.SQL, want)
		}
		if len(compiled.Args) != 1 || compiled.Args[0] != value {
			t.Fatalf("value was not isolated in bound args: %#v", compiled.Args)
		}
	})
}
