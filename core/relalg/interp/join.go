package interp

import (
	"errors"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// ErrJoinCardinality is returned when a join matches more than one row of
// its right side, which the IR rules out (it joins on a key).
var ErrJoinCardinality = errors.New("interp: a join matched more than one row")

// evalJoin extends each input row with the right row matching it (equal,
// non-NULL join columns and a TRUE right predicate), or with NULLs.
func evalJoin(db DB, n relalg.Join) (relation, error) {
	in, err := eval(db, n.Input)
	if err != nil {
		return relation{}, err
	}
	right, err := eval(db, relalg.Scan{Relation: n.Right})
	if err != nil {
		return relation{}, err
	}
	out := relation{Cols: append([]string(nil), in.Cols...), hidden: in.hidden + len(right.Cols)}
	for _, c := range right.Cols {
		out.Cols = append(out.Cols, n.Alias+"."+c)
	}
	for _, row := range in.Rows {
		matches, err := matching(in, row, right, n.On, n.RightPredicate)
		if err != nil {
			return relation{}, err
		}
		if len(matches) > 1 {
			return relation{}, fmt.Errorf("%w: alias %q", ErrJoinCardinality, n.Alias)
		}
		extended := append([]any(nil), row...)
		if len(matches) == 1 {
			extended = append(extended, matches[0]...)
		} else {
			extended = append(extended, make([]any, len(right.Cols))...)
		}
		out.Rows = append(out.Rows, extended)
	}
	return out, nil
}

// evalExists: TRUE when a row of the relation matches, else FALSE (never
// UNKNOWN: a NULL join column matches nothing).
func evalExists(outer relation, row []any, e relalg.Exists) (tri, error) {
	inner, err := eval(outer.db, relalg.Scan{Relation: e.Relation})
	if err != nil {
		return triUnknown, err
	}
	inner.db = outer.db
	matches, err := matching(outer, row, inner, e.On, e.Predicate)
	if err != nil {
		return triUnknown, err
	}
	return boolTri(len(matches) > 0), nil
}

// matching are the rows of right whose On columns equal row's (NULL equals
// nothing) and for which pred is TRUE.
func matching(left relation, row []any, right relation, on []relalg.JoinPair, pred relalg.Predicate) (
	[][]any, error,
) {
	var out [][]any
	for _, candidate := range right.Rows {
		ok := true
		for _, p := range on {
			l, err := left.colIndex(p.Left)
			if err != nil {
				return nil, err
			}
			r, err := right.colIndex(p.Right)
			if err != nil {
				return nil, err
			}
			eq, err := evalComparison(row[l], relalg.OpEq, candidate[r])
			if err != nil {
				return nil, err
			}
			ok = ok && eq == triTrue
		}
		if !ok {
			continue
		}
		v, err := evalPredicate(right, candidate, pred)
		if err != nil {
			return nil, err
		}
		if v == triTrue {
			out = append(out, candidate)
		}
	}
	return out, nil
}
