package interp

import (
	"errors"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// Verdict is the outcome of checking a predicate against a row of which only
// some columns are known.
type Verdict int

const (
	// Fails: the predicate is FALSE or UNKNOWN whatever the other columns
	// hold, so the row does not satisfy it.
	Fails Verdict = iota
	// Holds: the predicate is TRUE whatever the other columns hold.
	Holds
	// Depends: the outcome depends on columns that are not known; the
	// residual predicate says how.
	Depends
)

// errMixed is a residual that would combine an UNKNOWN constant with
// predicates on unknown columns, which the IR cannot express.
var errMixed = errors.New("interp: NULL compared beside unknown columns")

// Check evaluates p against a row whose known columns are given (a nil
// value is NULL), with the semantics of docs/design/ir-semantics.md. The
// conditions on known columns are decided; the rest of p is returned as the
// residual when the verdict is Depends. A predicate the check cannot decide
// exactly (EXISTS, incomparable values, NULL beside unknown columns) Fails,
// with the reason as the error: the caller refuses the row.
func Check(p relalg.Predicate, known map[string]any) (relalg.Predicate, Verdict, error) {
	r, err := partialEval(p, known)
	if err != nil {
		return nil, Fails, err
	}
	switch {
	case r.pred != nil:
		return r.pred, Depends, nil
	case r.v == triTrue:
		return nil, Holds, nil
	default:
		return nil, Fails, nil
	}
}

// partial is a decided value (pred nil) or a residual predicate.
type partial struct {
	v    tri
	pred relalg.Predicate
}

func partialEval(p relalg.Predicate, known map[string]any) (partial, error) {
	switch pp := p.(type) {
	case nil:
		return partial{v: triTrue}, nil
	case relalg.Condition:
		left, ok := known[pp.Field]
		if !ok {
			return partial{pred: pp}, nil
		}
		v, err := evalOp(left, pp)
		return partial{v: v}, err
	case relalg.And:
		return partialList(pp.Preds, known, triFalse, func(ps []relalg.Predicate) relalg.Predicate {
			return relalg.And{Preds: ps}
		})
	case relalg.Or:
		return partialList(pp.Preds, known, triTrue, func(ps []relalg.Predicate) relalg.Predicate {
			return relalg.Or{Preds: ps}
		})
	case relalg.Not:
		r, err := partialEval(pp.P, known)
		if err != nil || r.pred == nil {
			return partial{v: triNot(r.v)}, err
		}
		return partial{pred: relalg.Not{P: r.pred}}, nil
	default:
		return partial{}, fmt.Errorf("%w: predicate %T in a partial check", ErrUnsupported, p)
	}
}

// partialList folds an AND (dominant FALSE) or an OR (dominant TRUE).
func partialList(
	preds []relalg.Predicate,
	known map[string]any,
	dominant tri,
	join func([]relalg.Predicate) relalg.Predicate,
) (partial, error) {
	var residual []relalg.Predicate
	unknown := false
	for _, p := range preds {
		r, err := partialEval(p, known)
		if err != nil {
			return partial{}, err
		}
		switch {
		case r.pred != nil:
			residual = append(residual, r.pred)
		case r.v == dominant:
			return partial{v: dominant}, nil
		case r.v == triUnknown:
			unknown = true
		}
	}
	switch {
	case len(residual) == 0 && unknown:
		return partial{v: triUnknown}, nil
	case len(residual) == 0:
		return partial{v: triNot(dominant)}, nil
	case unknown:
		return partial{}, errMixed
	case len(residual) == 1:
		return partial{pred: residual[0]}, nil
	default:
		return partial{pred: join(residual)}, nil
	}
}
