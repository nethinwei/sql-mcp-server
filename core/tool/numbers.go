package tool

import (
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// A JSON number with a fraction or an exponent stays a json.Number through
// input decoding (see normalizeJSONValue): read as a float64 it would lose
// what a decimal column keeps (0.1, or amounts past 15 significant digits).
// Where the column is known, the number takes the column's kind; elsewhere
// it reaches the driver as its literal text, which the database converts.

// numberKind is how a column takes a JSON number.
type numberKind int

const (
	exactNumber   numberKind = iota // numeric, decimal, text, or not known: the literal text
	floatNumber                     // real, float, double: a float64
	integerNumber                   // an integer type: a fraction is invalid input
)

// numberKindOf classifies a column's database type (as introspection reports
// it, such as "integer", "double precision", "decimal(10,2)").
func numberKindOf(domainType string) numberKind {
	base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(domainType)), "(")
	base, _, _ = strings.Cut(base, " ")
	switch {
	case slices.Contains([]string{"real", "float", "float4", "float8", "double"}, base):
		return floatNumber
	case slices.Contains([]string{"smallint", "integer", "int", "bigint", "tinyint", "mediumint", "int2", "int4",
		"int8", "serial", "bigserial", "smallserial"}, base):
		return integerNumber
	}
	return exactNumber
}

// isDecimalType reports a numeric or decimal column, which drivers return as
// text.
func isDecimalType(domainType string) bool {
	t := strings.ToLower(domainType)
	return strings.HasPrefix(t, "numeric") || strings.HasPrefix(t, "decimal")
}

// coerceNumber converts v (a value, or a list of them) for column of e. The
// column's type is looked up once, at the first number: an IN list holds
// thousands.
func coerceNumber(e entity.Entity, column string, v any) (any, error) {
	kind, known := exactNumber, false
	numberKind := func() numberKind {
		if !known {
			if i := slices.IndexFunc(e.Attributes, func(a entity.Attribute) bool { return a.Name == column }); i >= 0 {
				kind = numberKindOf(e.Attributes[i].Domain.Type)
			}
			known = true
		}
		return kind
	}
	return coerceAs(numberKind, column, v)
}

func coerceAs(kind func() numberKind, column string, v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		switch kind() {
		case floatNumber:
			f, err := x.Float64()
			if err != nil {
				return nil, fmt.Errorf("%w: field %q: invalid number %q", ErrInvalidInput, column, x)
			}
			return f, nil
		case integerNumber:
			return nil, fmt.Errorf("%w: field %q holds integers, not %s", ErrInvalidInput, column, x)
		}
		return x.String(), nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			c, err := coerceAs(kind, column, item)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	}
	return v, nil
}

// coerceValues converts the numbers of values, keyed by field, in place.
func coerceValues(e entity.Entity, values map[string]any) error {
	for field, v := range values {
		c, err := coerceNumber(e, columnOf(e, field), v)
		if err != nil {
			return err
		}
		values[field] = c
	}
	return nil
}

// coercePredicate converts the numbers of p's conditions, which name columns
// of e. An EXISTS names its target's columns: its numbers are converted
// where it is resolved (see resolveExists).
func coercePredicate(e entity.Entity, p relalg.Predicate) (relalg.Predicate, error) {
	switch pp := p.(type) {
	case relalg.Condition:
		v, err := coerceNumber(e, pp.Field, pp.Value)
		pp.Value = v
		return pp, err
	case relalg.And:
		preds, err := coercePredicates(e, pp.Preds)
		return relalg.And{Preds: preds}, err
	case relalg.Or:
		preds, err := coercePredicates(e, pp.Preds)
		return relalg.Or{Preds: preds}, err
	case relalg.Not:
		inner, err := coercePredicate(e, pp.P)
		return relalg.Not{P: inner}, err
	}
	return p, nil
}

func coercePredicates(e entity.Entity, preds []relalg.Predicate) ([]relalg.Predicate, error) {
	out := make([]relalg.Predicate, len(preds))
	for i, p := range preds {
		c, err := coercePredicate(e, p)
		if err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}

// literalNumbers turns the json.Number arguments no column claimed (a
// procedure's, a joined field's) into their literal text, in place.
func literalNumbers(args []any) {
	for i, a := range args {
		switch x := a.(type) {
		case json.Number:
			args[i] = x.String()
		case []any:
			literalNumbers(x)
		}
	}
}

// exactValue is v as the reference interpreter compares it: the text of a
// decimal column as the exact number it holds.
func exactValue(domainType string, v any) any {
	s, ok := v.(string)
	if !ok || !isDecimalType(domainType) {
		return v
	}
	if r, ok := new(big.Rat).SetString(s); ok {
		return r
	}
	return v
}

// NormalizeSubject converts subject attributes decoded with UseNumber (a
// trusted proxy's X-MCP-Subject) to the values configured subjects and tool
// inputs carry: integers as int64, other numbers as their literal text. An
// attribute is a scalar (string, number, boolean, null) or a list of them,
// what a row filter can compare a column with; anything else is refused.
func NormalizeSubject(attrs map[string]any) error {
	for name, v := range attrs {
		n, err := subjectValue(v, true)
		if err != nil {
			return fmt.Errorf("subject attribute %q: %w", name, err)
		}
		attrs[name] = n
	}
	return nil
}

func subjectValue(v any, list bool) (any, error) {
	switch x := v.(type) {
	case nil, string, bool:
		return v, nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, nil
		}
		return x.String(), nil
	case []any:
		if !list {
			return nil, fmt.Errorf("a list holds scalars only")
		}
		out := make([]any, len(x))
		for i, item := range x {
			n, err := subjectValue(item, false)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	}
	return nil, fmt.Errorf("want a string, number, boolean or a list of them, got %T", v)
}
