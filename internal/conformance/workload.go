package conformance

import (
	"context"
	"fmt"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/relalg/interp"
	"github.com/nethinwei/sql-mcp-server/core/store"
	workload "github.com/nethinwei/sql-mcp-server/fixtures/v4/generator"
)

// SetupWorkload loads the fixtures/v4 business workload into db under the
// given schema (empty for the connection's default database). The generator
// renders the same deterministic data for every dialect; statements run one
// by one (MySQL-family drivers reject multi-statement strings).
func SetupWorkload(ctx context.Context, db store.DB, d dialect.Dialect, cfg workload.Config, schema string) error {
	dialectName := d.Name()
	if dialectName == "oceanbase" {
		dialectName = workload.DialectMySQL // identical rendering
	}
	for _, stmt := range workload.Generate(cfg).StatementsIn(dialectName, schema) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("workload setup: %w", err)
		}
	}
	return nil
}

// RunWorkload checks the workload fixture differentially: for every table,
// a checksum aggregate (row count, per-numeric-column sums, per-nullable-
// column non-NULL counts) is evaluated by the reference interpreter over
// the generated rows and by the provider through codegen. Equal results on
// all providers prove the same governed queries see the same logical data
// (design doc acceptance #6). String content and date values stay out of
// the checksums: collation and date rendering are documented deviations.
func RunWorkload(t *testing.T, db store.DB, d dialect.Dialect, cfg workload.Config, schema string) {
	t.Helper()
	dataset := workload.Generate(cfg)
	interpDB := workloadInterpDB(dataset)
	for _, table := range dataset.Tables {
		expr := checksumExpr(table, schema)
		t.Run(table.Name, func(t *testing.T) {
			want, err := interp.Eval(interpDB, expr)
			if err != nil {
				t.Fatalf("interpreter: %v", err)
			}
			compiled, err := codegen.Renderer{Dialect: d}.Compile(expr)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got, err := queryRows(db, compiled.SQL, compiled.Args)
			if err != nil {
				t.Fatalf("execute %s: %v", compiled.SQL, err)
			}
			if err := compareRows(want.Rows, got, false); err != nil {
				t.Fatalf("divergence on %s\nsql: %s\n%v", table.Name, compiled.SQL, err)
			}
		})
	}
}

// checksumExpr builds the per-table checksum aggregate: count(*) plus
// sum(col) for numeric columns and count(col) for nullable columns.
func checksumExpr(table *workload.Table, schema string) relalg.Expr {
	calls := []relalg.AggCall{{Func: relalg.AggCount}}
	for _, col := range table.Columns {
		switch {
		case col.Type == workload.ColInt || col.Type == workload.ColBigInt:
			calls = append(calls, relalg.AggCall{Func: relalg.AggSum, Field: col.Name})
		case col.Nullable:
			calls = append(calls, relalg.AggCall{Func: relalg.AggCount, Field: col.Name})
		}
	}
	return relalg.Aggregate{
		Input:      relalg.Scan{Relation: relalg.RelationRef{Schema: schema, Name: table.Name}},
		Aggregates: calls,
	}
}

// workloadInterpDB converts the generated dataset into an interpreter DB.
// Numbers become int64; dates and datetimes become strings (they only ever
// feed count aggregates, never value comparisons).
func workloadInterpDB(dataset *workload.Dataset) interp.DB {
	db := interp.DB{}
	for _, table := range dataset.Tables {
		cols := make([]string, len(table.Columns))
		for i, c := range table.Columns {
			cols[i] = c.Name
		}
		out := interp.Table{Cols: cols}
		for _, row := range table.Rows {
			converted := make([]any, len(row))
			for i, v := range row {
				converted[i] = interpValue(v)
			}
			out.Rows = append(out.Rows, converted)
		}
		db[table.Name] = out
	}
	return db
}

func interpValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int:
		return int64(x)
	case int64:
		return x
	case string:
		return x
	default:
		// time.Time and anything else: stringly typed, count-only.
		return fmt.Sprintf("%v", x)
	}
}
