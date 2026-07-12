// Package interp is the reference interpreter for the committed read-path
// subset of the relalg IR. It implements docs/design/ir-semantics.md verbatim
// and serves as the oracle for the cross-provider differential conformance
// suite (internal/conformance). It is not a performance reference.
package interp

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// ErrUnsupported is returned for IR nodes outside the committed read-path
// subset (writes, procedures).
var ErrUnsupported = errors.New("interp: unsupported expression")

// ErrUnknownColumn is returned when an expression references a column the
// input relation does not have.
var ErrUnknownColumn = errors.New("interp: unknown column")

// ErrUnknownRelation is returned when a Scan references a table not present
// in the DB.
var ErrUnknownRelation = errors.New("interp: unknown relation")

// Table is an in-memory base relation. Rows hold values in Cols order; cell
// values are nil, integers, floats, decimal strings are NOT allowed — use
// int64/float64/string/bool only, with numeric semantics per the spec.
type Table struct {
	Cols []string
	Rows [][]any
}

// DB maps relation names to tables. Schema qualifiers are ignored: the
// conformance fixture lives in a single schema.
type DB map[string]Table

// Result is an evaluated relation: column names and positional rows.
type Result struct {
	Cols []string
	Rows [][]any
}

// Eval evaluates a committed read-path expression against db.
func Eval(db DB, e relalg.Expr) (Result, error) {
	r, err := eval(db, e)
	if err != nil {
		return Result{}, err
	}
	return Result(r), nil
}

type relation struct {
	Cols []string
	Rows [][]any
}

func (r relation) colIndex(name string) (int, error) {
	for i, c := range r.Cols {
		if c == name {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownColumn, name)
}

func eval(db DB, e relalg.Expr) (relation, error) {
	switch n := e.(type) {
	case relalg.Scan:
		t, ok := db[n.Relation.Name]
		if !ok {
			return relation{}, fmt.Errorf("%w: %q", ErrUnknownRelation, n.Relation.Name)
		}
		return relation{Cols: t.Cols, Rows: t.Rows}, nil
	case relalg.Select:
		return evalSelect(db, n)
	case relalg.Project:
		return evalProject(db, n)
	case relalg.Aggregate:
		return evalAggregate(db, n)
	case relalg.Sort:
		return evalSort(db, n)
	case relalg.Limit:
		return evalLimit(db, n)
	case relalg.Distinct:
		return evalDistinct(db, n)
	default:
		return relation{}, fmt.Errorf("%w: %T", ErrUnsupported, e)
	}
}

// evalSelect keeps only rows where the predicate evaluates to TRUE; both
// FALSE and UNKNOWN are filtered out (three-valued logic).
func evalSelect(db DB, n relalg.Select) (relation, error) {
	in, err := eval(db, n.Input)
	if err != nil {
		return relation{}, err
	}
	if err := relalg.ValidatePredicate(n.Predicate); err != nil {
		return relation{}, err
	}
	out := relation{Cols: in.Cols}
	for _, row := range in.Rows {
		v, err := evalPredicate(in, row, n.Predicate)
		if err != nil {
			return relation{}, err
		}
		if v == triTrue {
			out.Rows = append(out.Rows, row)
		}
	}
	return out, nil
}

func evalProject(db DB, n relalg.Project) (relation, error) {
	in, err := eval(db, n.Input)
	if err != nil {
		return relation{}, err
	}
	idx := make([]int, len(n.Items))
	cols := make([]string, len(n.Items))
	for i, item := range n.Items {
		j, err := in.colIndex(item.Field)
		if err != nil {
			return relation{}, err
		}
		idx[i] = j
		cols[i] = item.Field
		if item.Alias != "" {
			cols[i] = item.Alias
		}
	}
	out := relation{Cols: cols, Rows: make([][]any, len(in.Rows))}
	for r, row := range in.Rows {
		projected := make([]any, len(idx))
		for i, j := range idx {
			projected[i] = row[j]
		}
		out.Rows[r] = projected
	}
	return out, nil
}

// evalSort orders rows by the terms. NULL placement is provider-defined per
// the spec; the interpreter canonically puts NULL first ascending and last
// descending, and the equivalence corpus avoids NULL sort keys. Rows equal
// under all terms keep no guaranteed relative order (stable here).
func evalSort(db DB, n relalg.Sort) (relation, error) {
	in, err := eval(db, n.Input)
	if err != nil {
		return relation{}, err
	}
	idx := make([]int, len(n.OrderBy))
	for i, t := range n.OrderBy {
		j, err := in.colIndex(t.Field)
		if err != nil {
			return relation{}, err
		}
		idx[i] = j
	}
	rows := append([][]any(nil), in.Rows...)
	var sortErr error
	sort.SliceStable(rows, func(a, b int) bool {
		for i, t := range n.OrderBy {
			c, err := compareForSort(rows[a][idx[i]], rows[b][idx[i]])
			if err != nil && sortErr == nil {
				sortErr = err
			}
			if strings.EqualFold(t.Dir, "desc") {
				c = -c
			}
			if c != 0 {
				return c < 0
			}
		}
		return false
	})
	if sortErr != nil {
		return relation{}, sortErr
	}
	return relation{Cols: in.Cols, Rows: rows}, nil
}

func evalLimit(db DB, n relalg.Limit) (relation, error) {
	in, err := eval(db, n.Input)
	if err != nil {
		return relation{}, err
	}
	start := n.Offset
	if start < 0 {
		start = 0
	}
	if start > int64(len(in.Rows)) {
		start = int64(len(in.Rows))
	}
	end := start + n.Count
	if n.Count < 0 || end > int64(len(in.Rows)) {
		end = int64(len(in.Rows))
	}
	return relation{Cols: in.Cols, Rows: in.Rows[start:end]}, nil
}

// evalDistinct deduplicates whole rows. NULLs compare equal to each other
// (distinct semantics, not = semantics). First occurrence order is kept.
func evalDistinct(db DB, n relalg.Distinct) (relation, error) {
	in, err := eval(db, n.Input)
	if err != nil {
		return relation{}, err
	}
	seen := map[string]bool{}
	out := relation{Cols: in.Cols}
	for _, row := range in.Rows {
		key, err := rowKey(row)
		if err != nil {
			return relation{}, err
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}
