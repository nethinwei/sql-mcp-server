package interp

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// ErrIncomparable is returned when two non-NULL values of incompatible types
// are compared. The conformance fixture is typed, so this always indicates a
// corpus or interpreter bug rather than a data condition.
var ErrIncomparable = errors.New("interp: incomparable values")

// toRat converts a numeric value to an exact rational. Decimal strings are
// not accepted: fixture cells must be typed Go numerics.
func toRat(v any) (*big.Rat, bool) {
	switch n := v.(type) {
	case int:
		return new(big.Rat).SetInt64(int64(n)), true
	case int32:
		return new(big.Rat).SetInt64(int64(n)), true
	case int64:
		return new(big.Rat).SetInt64(n), true
	case float64:
		return new(big.Rat).SetFloat64(n), true
	case *big.Rat:
		return n, true
	}
	return nil, false
}

// compareValues compares two non-NULL values: numerics numerically, strings
// bytewise, booleans false<true.
func compareValues(a, b any) (int, error) {
	if ra, ok := toRat(a); ok {
		rb, ok := toRat(b)
		if !ok {
			return 0, fmt.Errorf("%w: %T vs %T", ErrIncomparable, a, b)
		}
		return ra.Cmp(rb), nil
	}
	if sa, ok := a.(string); ok {
		sb, ok := b.(string)
		if !ok {
			return 0, fmt.Errorf("%w: %T vs %T", ErrIncomparable, a, b)
		}
		return strings.Compare(sa, sb), nil
	}
	if ba, ok := a.(bool); ok {
		bb, ok := b.(bool)
		if !ok {
			return 0, fmt.Errorf("%w: %T vs %T", ErrIncomparable, a, b)
		}
		return boolCmp(ba, bb), nil
	}
	return 0, fmt.Errorf("%w: %T vs %T", ErrIncomparable, a, b)
}

func boolCmp(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}

// compareForSort orders values inside Sort: NULL first (the spec leaves NULL
// placement provider-defined; the equivalence corpus avoids NULL sort keys).
func compareForSort(a, b any) (int, error) {
	switch {
	case a == nil && b == nil:
		return 0, nil
	case a == nil:
		return -1, nil
	case b == nil:
		return 1, nil
	}
	return compareValues(a, b)
}

// valueKey encodes a value into a canonical string for distinct/group-by
// hashing, where NULL equals NULL.
func valueKey(v any) (string, error) {
	if v == nil {
		return "n", nil
	}
	if r, ok := toRat(v); ok {
		return "r:" + r.RatString(), nil
	}
	if s, ok := v.(string); ok {
		return "s:" + s, nil
	}
	if b, ok := v.(bool); ok {
		if b {
			return "b:1", nil
		}
		return "b:0", nil
	}
	return "", fmt.Errorf("%w: %T", ErrIncomparable, v)
}

func rowKey(row []any) (string, error) {
	parts := make([]string, len(row))
	for i, v := range row {
		k, err := valueKey(v)
		if err != nil {
			return "", err
		}
		parts[i] = k
	}
	return strings.Join(parts, "\x00"), nil
}
