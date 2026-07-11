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

// ErrInvalidPredicate is returned when a known predicate has an empty or
// otherwise malformed shape.
var ErrInvalidPredicate = errors.New("relalg: invalid predicate")

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
		return validateCondition(pp, maxIN)
	case And:
		return validatePredicateList("AND", pp.Preds, maxIN)
	case Or:
		return validatePredicateList("OR", pp.Preds, maxIN)
	case Not:
		if pp.P == nil {
			return fmt.Errorf("%w: nil NOT operand", ErrInvalidPredicate)
		}
		return ValidatePredicateWithMaxINCardinality(pp.P, maxIN)
	case IsNull:
		if pp.Field == "" {
			return fmt.Errorf("%w: empty field", ErrInvalidPredicate)
		}
		return nil
	default:
		return fmt.Errorf("%w: %T", ErrUnknownPredicate, p)
	}
}

func validateCondition(condition Condition, maxIN int) error {
	if condition.Field == "" {
		return fmt.Errorf("%w: empty field", ErrInvalidPredicate)
	}
	if !condition.Op.Valid() {
		return fmt.Errorf("%w: %q on field %q", ErrInvalidOp, condition.Op, condition.Field)
	}
	if (condition.Op == OpIsNull || condition.Op == OpIsNotNull) && condition.Value != nil {
		return fmt.Errorf("%w: %s does not accept a value", ErrInvalidPredicate, condition.Op)
	}
	if condition.Op != OpIn && condition.Op != OpNotIn {
		return nil
	}
	values, ok := condition.Value.([]any)
	if !ok {
		return fmt.Errorf("%w: IN value must be []any, got %T", ErrINCardinality, condition.Value)
	}
	if len(values) == 0 || len(values) > maxIN {
		return fmt.Errorf("%w: got %d values, maximum %d", ErrINCardinality, len(values), maxIN)
	}
	return nil
}

func validatePredicateList(name string, predicates []Predicate, maxIN int) error {
	if len(predicates) == 0 {
		return fmt.Errorf("%w: empty %s", ErrInvalidPredicate, name)
	}
	for _, predicate := range predicates {
		if predicate == nil {
			return fmt.Errorf("%w: nil %s operand", ErrInvalidPredicate, name)
		}
		if err := ValidatePredicateWithMaxINCardinality(predicate, maxIN); err != nil {
			return err
		}
	}
	return nil
}
