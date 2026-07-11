package relalg

import (
	"errors"
	"fmt"
)

// ErrInvalidOp is returned when a predicate uses an operator outside the
// whitelist.
var ErrInvalidOp = errors.New("relalg: invalid operator")

// ErrInvalidAggFunc is returned when an aggregate uses a function outside the
// whitelist.
var ErrInvalidAggFunc = errors.New("relalg: invalid aggregate function")

// ErrUnknownPredicate is returned when ValidatePredicate encounters a predicate
// type it does not recognize.
var ErrUnknownPredicate = errors.New("relalg: unknown predicate")

// ErrINCardinality is returned when an IN-list is empty, malformed, or exceeds
// the configured bound.
var ErrINCardinality = errors.New("relalg: invalid IN cardinality")

// DefaultMaxINCardinality is the safe default used by ValidatePredicate.
const DefaultMaxINCardinality = 1000

// ValidatePredicate recursively checks that every Condition uses a whitelisted
// operator. It returns nil for a nil predicate. Codegen calls this before
// rendering so malformed input fails early rather than producing bad SQL.
func ValidatePredicate(p Predicate) error {
	return ValidatePredicateWithMaxINCardinality(p, DefaultMaxINCardinality)
}

// ValidatePredicateWithMaxINCardinality validates a predicate and bounds every
// IN/NOT IN list. maxIN must be positive; zero never means unbounded.
func ValidatePredicateWithMaxINCardinality(p Predicate, maxIN int) error {
	if maxIN <= 0 {
		return fmt.Errorf("%w: maximum must be positive", ErrINCardinality)
	}
	switch pp := p.(type) {
	case nil:
		return nil
	case Condition:
		if !pp.Op.Valid() {
			return fmt.Errorf("%w: %q on field %q", ErrInvalidOp, pp.Op, pp.Field)
		}
		if pp.Op == OpIn || pp.Op == OpNotIn {
			values, ok := pp.Value.([]any)
			if !ok {
				return fmt.Errorf("%w: IN value must be []any, got %T", ErrINCardinality, pp.Value)
			}
			if len(values) == 0 || len(values) > maxIN {
				return fmt.Errorf("%w: got %d values, maximum %d", ErrINCardinality, len(values), maxIN)
			}
		}
		return nil
	case And:
		for _, q := range pp.Preds {
			if err := ValidatePredicateWithMaxINCardinality(q, maxIN); err != nil {
				return err
			}
		}
		return nil
	case Or:
		for _, q := range pp.Preds {
			if err := ValidatePredicateWithMaxINCardinality(q, maxIN); err != nil {
				return err
			}
		}
		return nil
	case Not:
		return ValidatePredicateWithMaxINCardinality(pp.P, maxIN)
	case IsNull:
		return nil
	default:
		return fmt.Errorf("%w: %T", ErrUnknownPredicate, p)
	}
}
