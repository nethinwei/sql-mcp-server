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

// ValidatePredicate recursively checks that every Condition uses a whitelisted
// operator. It returns nil for a nil predicate. Codegen calls this before
// rendering so malformed input fails early rather than producing bad SQL.
func ValidatePredicate(p Predicate) error {
	switch pp := p.(type) {
	case nil:
		return nil
	case Condition:
		if !pp.Op.Valid() {
			return fmt.Errorf("%w: %q on field %q", ErrInvalidOp, pp.Op, pp.Field)
		}
		return nil
	case And:
		for _, q := range pp.Preds {
			if err := ValidatePredicate(q); err != nil {
				return err
			}
		}
		return nil
	case Or:
		for _, q := range pp.Preds {
			if err := ValidatePredicate(q); err != nil {
				return err
			}
		}
		return nil
	case Not:
		return ValidatePredicate(pp.P)
	case IsNull:
		return nil
	default:
		return fmt.Errorf("%w: %T", ErrUnknownPredicate, p)
	}
}
