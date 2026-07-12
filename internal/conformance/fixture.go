// Package conformance is the cross-provider differential conformance suite
// for the committed read-path IR subset. Every case is evaluated by the
// reference interpreter (core/relalg/interp, the oracle defined by
// docs/design/ir-semantics.md) and by a real provider through
// codegen.Renderer; results must be equal after canonical normalization.
//
// The equivalence corpus deliberately avoids the documented deviations
// listed in the spec (NULL sort placement, collation case folding,
// non-terminating averages, type-boundary overflow).
package conformance

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/relalg/interp"
)

// TableName is the single fixture relation.
const TableName = "conf_t"

// fixtureRow keeps the fixture in one typed place so the interpreter table
// and the SQL INSERT are generated from identical values. price is a decimal
// string with two digits ("" for NULL) so no float formatting is involved.
type fixtureRow struct {
	id    int64
	cat   any // string or nil
	val   any // int64 or nil
	price string
	name  string
}

// fixtureRows is deterministic and hand-checked: every aggregate case in the
// corpus terminates within two decimal digits (see the spec's avg deviation).
func fixtureRows() []fixtureRow {
	return []fixtureRow{
		{1, "a", int64(10), "1.25", "alpha"},
		{2, "b", nil, "2.50", "beta"},
		{3, nil, int64(-5), "", "gamma"},
		{4, "a", int64(10), "3.75", "delta"},
		{5, "b", int64(0), "0.25", "epsilon"},
		{6, nil, int64(7), "", "zeta"},
		{7, "a", nil, "5.00", "eta"},
		{8, "b", int64(3), "2.50", "theta"},
		{9, nil, int64(10), "", "iota"},
		{10, "a", int64(-5), "", "kappa"},
		{11, "b", int64(9), "4.25", "lambda"},
		{12, nil, int64(2), "0.50", "mu"},
	}
}

// Fixture returns the fixture as an interpreter DB.
func Fixture() interp.DB {
	rows := fixtureRows()
	table := interp.Table{Cols: []string{"id", "cat", "val", "price", "name"}}
	for _, r := range rows {
		var price any
		if r.price != "" {
			rat, ok := new(big.Rat).SetString(r.price)
			if !ok {
				panic("conformance: bad fixture price " + r.price)
			}
			price = rat
		}
		table.Rows = append(table.Rows, []any{r.id, r.cat, r.val, price, r.name})
	}
	return interp.DB{TableName: table}
}

// Qualified returns the schema-qualified table name ("schema.conf_t"), or
// the bare name when schema is empty.
func Qualified(schema string) string {
	if schema == "" {
		return TableName
	}
	return schema + "." + TableName
}

// scan returns the fixture Scan node for the given schema.
func scan(schema string) relalg.Expr {
	return relalg.Scan{Relation: relalg.RelationRef{Schema: schema, Name: TableName}}
}

// SetupStatements returns the DDL and seed statements for the given dialect
// family ("postgres" or the MySQL family used by MySQL and OceanBase). Each
// statement must be executed separately: MySQL drivers reject
// multi-statement strings by default.
func SetupStatements(dialectName, schema string) []string {
	qualified := Qualified(schema)
	var stmts []string
	if schema != "" && dialectName != "postgres" {
		stmts = append(stmts, "CREATE DATABASE IF NOT EXISTS "+schema)
	}
	stmts = append(stmts, "DROP TABLE IF EXISTS "+qualified)
	if dialectName == "postgres" {
		stmts = append(stmts, "CREATE TABLE "+qualified+
			" (id integer PRIMARY KEY, cat text, val integer, price numeric(10,2), name text)")
	} else {
		stmts = append(stmts, "CREATE TABLE "+qualified+
			" (id int PRIMARY KEY, cat varchar(16), val int, price decimal(10,2), name varchar(32))")
	}
	return append(stmts, insertStatement(qualified))
}

// insertStatement renders one multi-row INSERT from the fixture. All values
// are produced by this package (no external input), so literals are safe.
func insertStatement(qualified string) string {
	var tuples []string
	for _, r := range fixtureRows() {
		tuple := fmt.Sprintf("(%d, %s, %s, %s, '%s')",
			r.id, sqlLiteral(r.cat), sqlLiteral(r.val), sqlDecimal(r.price), r.name)
		tuples = append(tuples, tuple)
	}
	return "INSERT INTO " + qualified + " (id, cat, val, price, name) VALUES " +
		strings.Join(tuples, ", ")
}

func sqlLiteral(v any) string {
	switch n := v.(type) {
	case nil:
		return "NULL"
	case int64:
		return fmt.Sprintf("%d", n)
	case string:
		return "'" + n + "'"
	default:
		panic(fmt.Sprintf("conformance: unsupported fixture literal %T", v))
	}
}

func sqlDecimal(s string) string {
	if s == "" {
		return "NULL"
	}
	return s
}
