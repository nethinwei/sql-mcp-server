package relalg

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
