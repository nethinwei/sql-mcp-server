package interp

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// tri is a three-valued logic value per the IR semantics spec.
type tri int

const (
	triFalse tri = iota
	triUnknown
	triTrue
)

func triNot(v tri) tri {
	switch v {
	case triTrue:
		return triFalse
	case triFalse:
		return triTrue
	default:
		return triUnknown
	}
}

func evalPredicate(rel relation, row []any, p relalg.Predicate) (tri, error) {
	switch pp := p.(type) {
	case nil:
		return triTrue, nil
	case relalg.Condition:
		return evalCondition(rel, row, pp)
	case relalg.And:
		return evalAnd(rel, row, pp.Preds)
	case relalg.Or:
		return evalOr(rel, row, pp.Preds)
	case relalg.Not:
		v, err := evalPredicate(rel, row, pp.P)
		if err != nil {
			return triUnknown, err
		}
		return triNot(v), nil
	case relalg.IsNull:
		i, err := rel.colIndex(pp.Field)
		if err != nil {
			return triUnknown, err
		}
		isNull := row[i] == nil
		if isNull != pp.Not {
			return triTrue, nil
		}
		return triFalse, nil
	default:
		return triUnknown, fmt.Errorf("%w: predicate %T", ErrUnsupported, p)
	}
}

// evalAnd: FALSE dominates, then UNKNOWN, else TRUE.
func evalAnd(rel relation, row []any, preds []relalg.Predicate) (tri, error) {
	result := triTrue
	for _, p := range preds {
		v, err := evalPredicate(rel, row, p)
		if err != nil {
			return triUnknown, err
		}
		if v == triFalse {
			return triFalse, nil
		}
		if v == triUnknown {
			result = triUnknown
		}
	}
	return result, nil
}

// evalOr: TRUE dominates, then UNKNOWN, else FALSE.
func evalOr(rel relation, row []any, preds []relalg.Predicate) (tri, error) {
	result := triFalse
	for _, p := range preds {
		v, err := evalPredicate(rel, row, p)
		if err != nil {
			return triUnknown, err
		}
		if v == triTrue {
			return triTrue, nil
		}
		if v == triUnknown {
			result = triUnknown
		}
	}
	return result, nil
}

func evalCondition(rel relation, row []any, c relalg.Condition) (tri, error) {
	i, err := rel.colIndex(c.Field)
	if err != nil {
		return triUnknown, err
	}
	left := row[i]
	switch c.Op {
	case relalg.OpIsNull:
		return boolTri(left == nil), nil
	case relalg.OpIsNotNull:
		return boolTri(left != nil), nil
	case relalg.OpIn:
		return evalIn(left, c.Value)
	case relalg.OpNotIn:
		v, err := evalIn(left, c.Value)
		if err != nil {
			return triUnknown, err
		}
		return triNot(v), nil
	case relalg.OpLike:
		return evalLike(left, c.Value)
	default:
		return evalComparison(left, c.Op, c.Value)
	}
}

func boolTri(b bool) tri {
	if b {
		return triTrue
	}
	return triFalse
}

// evalComparison implements eq/ne/gt/gte/lt/lte: NULL on either side is
// UNKNOWN.
func evalComparison(left any, op relalg.Op, right any) (tri, error) {
	if left == nil || right == nil {
		return triUnknown, nil
	}
	c, err := compareValues(left, right)
	if err != nil {
		return triUnknown, err
	}
	switch op {
	case relalg.OpEq:
		return boolTri(c == 0), nil
	case relalg.OpNe:
		return boolTri(c != 0), nil
	case relalg.OpGt:
		return boolTri(c > 0), nil
	case relalg.OpGte:
		return boolTri(c >= 0), nil
	case relalg.OpLt:
		return boolTri(c < 0), nil
	case relalg.OpLte:
		return boolTri(c <= 0), nil
	default:
		return triUnknown, fmt.Errorf("%w: operator %q", ErrUnsupported, op)
	}
}

// evalIn implements x IN (v1..vn) as x=v1 OR ... OR x=vn: any TRUE wins,
// else any UNKNOWN (including NULLs in the list) yields UNKNOWN.
func evalIn(left any, value any) (tri, error) {
	values, ok := value.([]any)
	if !ok {
		return triUnknown, fmt.Errorf("%w: IN value %T", ErrUnsupported, value)
	}
	result := triFalse
	for _, v := range values {
		eq, err := evalComparison(left, relalg.OpEq, v)
		if err != nil {
			return triUnknown, err
		}
		if eq == triTrue {
			return triTrue, nil
		}
		if eq == triUnknown {
			result = triUnknown
		}
	}
	return result, nil
}

// evalLike is case-sensitive (canonical semantics; case-insensitive
// collations are a documented deviation). % matches any sequence, _ one
// character.
func evalLike(left any, pattern any) (tri, error) {
	if left == nil || pattern == nil {
		return triUnknown, nil
	}
	s, ok := left.(string)
	if !ok {
		return triUnknown, fmt.Errorf("%w: LIKE on %T", ErrIncomparable, left)
	}
	p, ok := pattern.(string)
	if !ok {
		return triUnknown, fmt.Errorf("%w: LIKE pattern %T", ErrIncomparable, pattern)
	}
	re, err := likeRegexp(p)
	if err != nil {
		return triUnknown, err
	}
	return boolTri(re.MatchString(s)), nil
}

func likeRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString(`(?s)\A`)
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString(`\z`)
	return regexp.Compile(b.String())
}
