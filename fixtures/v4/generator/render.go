package workload

import (
	"fmt"
	"strings"
	"time"
)

// Dialect names accepted by the renderer. MySQL and OceanBase share syntax.
const (
	DialectPostgres  = "postgres"
	DialectMySQL     = "mysql"
	DialectOceanBase = "oceanbase"
)

// insertBatch bounds rows per INSERT statement to keep statements portable.
const insertBatch = 100

// colDDL maps a logical column type to the dialect DDL type.
func colDDL(d string, t ColType) string {
	mysqlFamily := d == DialectMySQL || d == DialectOceanBase
	switch t {
	case ColInt:
		return "integer"
	case ColBigInt:
		return "bigint"
	case ColText:
		if mysqlFamily {
			return "varchar(255)"
		}
		return "text"
	case ColCode:
		return "varchar(64)"
	case ColDate:
		return "date"
	case ColDateTime:
		if mysqlFamily {
			return "datetime"
		}
		return "timestamp"
	}
	panic(fmt.Sprintf("unknown column type %d", t))
}

// Statements renders DROP/CREATE/INSERT statements for every table of the
// dataset in the given dialect, one statement per element (MySQL-family
// drivers reject multi-statement strings).
func (d *Dataset) Statements(dialect string) []string {
	return d.StatementsIn(dialect, "")
}

// StatementsIn renders the dataset with every table name qualified by
// schema (empty schema renders bare names; the MySQL family additionally
// gets a CREATE DATABASE for the schema).
func (d *Dataset) StatementsIn(dialect, schema string) []string {
	var out []string
	if schema != "" && dialect != DialectPostgres {
		out = append(out, "CREATE DATABASE IF NOT EXISTS "+schema)
	}
	for _, t := range d.Tables {
		out = append(out, t.StatementsIn(dialect, schema)...)
	}
	return out
}

// Qualified returns the schema-qualified physical name of the table.
func (t *Table) Qualified(schema string) string {
	if schema == "" {
		return t.Name
	}
	return schema + "." + t.Name
}

// StatementsIn renders one table's DDL and seed rows under a schema.
func (t *Table) StatementsIn(dialect, schema string) []string {
	name := t.Qualified(schema)
	out := []string{"DROP TABLE IF EXISTS " + name, t.createDDL(dialect, name)}
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = c.Name
	}
	for start := 0; start < len(t.Rows); start += insertBatch {
		end := min(start+insertBatch, len(t.Rows))
		var b strings.Builder
		fmt.Fprintf(&b, "INSERT INTO %s (%s) VALUES", name, strings.Join(cols, ", "))
		for i, row := range t.Rows[start:end] {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("\n(")
			for j, v := range row {
				if j > 0 {
					b.WriteString(", ")
				}
				b.WriteString(literal(v))
			}
			b.WriteString(")")
		}
		out = append(out, b.String())
	}
	return out
}

func (t *Table) createDDL(dialect, qualifiedName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", qualifiedName)
	for i, c := range t.Columns {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "\t%s %s", c.Name, colDDL(dialect, c.Type))
		if !c.Nullable {
			b.WriteString(" NOT NULL")
		}
	}
	if len(t.PrimaryKey) > 0 {
		fmt.Fprintf(&b, ",\n\tPRIMARY KEY (%s)", strings.Join(t.PrimaryKey, ", "))
	}
	b.WriteString("\n)")
	return b.String()
}

// literal renders one value as a SQL literal. Generated values are ints,
// int64s, strings, time.Time, or nil by construction.
func literal(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 {
			return "'" + fmtDate(x) + "'"
		}
		return "'" + fmtDateTime(x) + "'"
	}
	panic(fmt.Sprintf("unsupported literal type %T", v))
}
