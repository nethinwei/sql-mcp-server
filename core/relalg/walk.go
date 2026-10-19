package relalg

// MapConditions is p with each condition replaced by what f returns for it,
// through AND, OR and NOT. An EXISTS is kept as it is: its conditions name
// fields of its target, not p's.
func MapConditions(p Predicate, f func(Condition) Condition) Predicate {
	switch pp := p.(type) {
	case Condition:
		return f(pp)
	case And:
		return And{Preds: mapAll(pp.Preds, f)}
	case Or:
		return Or{Preds: mapAll(pp.Preds, f)}
	case Not:
		return Not{P: MapConditions(pp.P, f)}
	}
	return p
}

func mapAll(preds []Predicate, f func(Condition) Condition) []Predicate {
	out := make([]Predicate, len(preds))
	for i, p := range preds {
		out[i] = MapConditions(p, f)
	}
	return out
}

// Fields are the fields p's conditions name, each once, in the order they
// appear. An EXISTS names fields of its target, which are not p's.
func Fields(p Predicate) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(Predicate)
	walk = func(p Predicate) {
		switch pp := p.(type) {
		case Condition:
			if !seen[pp.Field] {
				seen[pp.Field] = true
				out = append(out, pp.Field)
			}
		case And:
			for _, q := range pp.Preds {
				walk(q)
			}
		case Or:
			for _, q := range pp.Preds {
				walk(q)
			}
		case Not:
			walk(pp.P)
		}
	}
	walk(p)
	return out
}
