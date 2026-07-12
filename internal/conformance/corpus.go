package conformance

import (
	"fmt"
	"math/rand"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// Case is one differential case: the same IR is evaluated by the reference
// interpreter and by a provider; results must be equal. Ordered cases carry
// a Sort whose keys are unique and non-NULL, so they compare in order;
// everything else compares as a bag.
type Case struct {
	Name    string
	Expr    relalg.Expr
	Ordered bool
}

// propertyCases is the number of seeded pseudo-random cases. The seed is
// fixed: the corpus is versioned and reproducible, not fuzzed.
const propertyCases = 60

// Cases returns the corpus against the fixture table in the given schema.
func Cases(schema string) []Case {
	cases := fixedCases(schema)
	return append(cases, generatedCases(schema)...)
}

func selCase(name, schema string, p relalg.Predicate) Case {
	return Case{Name: name, Expr: relalg.Select{Input: scan(schema), Predicate: p}}
}

func cond(field string, op relalg.Op, v any) relalg.Condition {
	return relalg.Condition{Field: field, Op: op, Value: v}
}

func fixedCases(schema string) []Case {
	cases := []Case{
		selCase("ne-skips-null", schema, cond("val", relalg.OpNe, int64(10))),
		selCase("not-does-not-resurrect-unknown", schema,
			relalg.Not{P: cond("val", relalg.OpGt, int64(0))}),
		selCase("in-with-null-in-list", schema, cond("val", relalg.OpIn, []any{int64(10), nil})),
		selCase("not-in-with-null-in-list", schema, cond("val", relalg.OpNotIn, []any{int64(10), nil})),
		selCase("is-null", schema, cond("cat", relalg.OpIsNull, nil)),
		selCase("is-not-null", schema, relalg.IsNull{Field: "cat", Not: true}),
		selCase("or-with-unknown-branch", schema, relalg.Or{Preds: []relalg.Predicate{
			cond("val", relalg.OpGt, int64(0)), cond("cat", relalg.OpEq, "b"),
		}}),
		selCase("and-with-unknown-branch", schema, relalg.And{Preds: []relalg.Predicate{
			cond("val", relalg.OpGte, int64(0)), cond("cat", relalg.OpEq, "b"),
		}}),
		selCase("like-prefix", schema, cond("name", relalg.OpLike, "%a")),
		selCase("like-underscore", schema, cond("name", relalg.OpLike, "_eta")),
		selCase("decimal-comparison", schema, cond("price", relalg.OpGte, 2.5)),
		selCase("range-and", schema, relalg.And{Preds: []relalg.Predicate{
			cond("val", relalg.OpGte, int64(0)), cond("val", relalg.OpLte, int64(9)),
		}}),
	}
	cases = append(cases, shapeCases(schema)...)
	return append(cases, aggregateCases(schema)...)
}

func shapeCases(schema string) []Case {
	projectCat := relalg.Project{Input: scan(schema), Items: []relalg.ProjectItem{{Field: "cat"}}}
	sorted := relalg.Sort{
		Input:   relalg.Select{Input: scan(schema), Predicate: relalg.IsNull{Field: "val", Not: true}},
		OrderBy: []relalg.OrderTerm{{Field: "val", Dir: "desc"}, {Field: "id", Dir: "asc"}},
	}
	return []Case{
		{Name: "projection-keeps-duplicates", Expr: projectCat},
		{Name: "distinct-collapses-nulls", Expr: relalg.Distinct{Input: projectCat}},
		{Name: "sort-limit-offset", Expr: relalg.Limit{Input: sorted, Count: 3, Offset: 2},
			Ordered: true},
		{Name: "offset-beyond-rows", Expr: relalg.Limit{Input: sorted, Count: 5, Offset: 50},
			Ordered: true},
		{Name: "project-alias", Expr: relalg.Project{Input: scan(schema),
			Items: []relalg.ProjectItem{{Field: "id", Alias: "k"}, {Field: "name"}}}},
	}
}

func aggregateCases(schema string) []Case {
	empty := relalg.Select{Input: scan(schema), Predicate: cond("id", relalg.OpGt, int64(100))}
	return []Case{
		{Name: "count-star-vs-count-field", Expr: relalg.Aggregate{Input: scan(schema),
			Aggregates: []relalg.AggCall{
				{Func: relalg.AggCount},
				{Func: relalg.AggCount, Field: "val"},
				{Func: relalg.AggCount, Field: "price"},
			}}},
		{Name: "global-aggregate-empty-input", Expr: relalg.Aggregate{Input: empty,
			Aggregates: []relalg.AggCall{
				{Func: relalg.AggCount},
				{Func: relalg.AggSum, Field: "val"},
				{Func: relalg.AggMax, Field: "val"},
			}}},
		{Name: "grouped-aggregate-empty-input", Expr: relalg.Aggregate{Input: empty,
			GroupBy: []string{"cat"}, Aggregates: []relalg.AggCall{{Func: relalg.AggCount}}}},
		{Name: "group-by-null-forms-one-group", Expr: relalg.Aggregate{Input: scan(schema),
			GroupBy: []string{"cat"}, Aggregates: []relalg.AggCall{{Func: relalg.AggCount}}}},
		{Name: "aggregates-skip-nulls", Expr: relalg.Aggregate{Input: scan(schema),
			Aggregates: []relalg.AggCall{
				{Func: relalg.AggSum, Field: "val"},
				{Func: relalg.AggMin, Field: "val"},
				{Func: relalg.AggMax, Field: "val"},
				{Func: relalg.AggAvg, Field: "val"},
			}}},
		{Name: "avg-decimal-terminating", Expr: relalg.Aggregate{Input: scan(schema),
			Aggregates: []relalg.AggCall{{Func: relalg.AggAvg, Field: "price"}}}},
		{Name: "sum-decimal-by-group", Expr: relalg.Aggregate{Input: scan(schema),
			GroupBy: []string{"cat"}, Aggregates: []relalg.AggCall{{Func: relalg.AggSum, Field: "price"}}}},
		{Name: "avg-int-by-group-terminating", Expr: relalg.Aggregate{Input: scan(schema),
			GroupBy: []string{"cat"}, Aggregates: []relalg.AggCall{{Func: relalg.AggAvg, Field: "val"}}}},
	}
}

// generatedCases derives bounded pseudo-random cases from a fixed seed. The
// generator only produces IR inside the equivalence corpus: type-correct
// predicates, sorts on the unique non-NULL id column, no avg (its precision
// is a documented deviation).
func generatedCases(schema string) []Case {
	rng := rand.New(rand.NewSource(1))
	cases := make([]Case, 0, propertyCases)
	for i := 0; i < propertyCases; i++ {
		pred := genPredicate(rng, 0)
		var c Case
		switch i % 4 {
		case 0:
			c = Case{Expr: relalg.Select{Input: scan(schema), Predicate: pred}}
		case 1:
			c = Case{Ordered: true, Expr: relalg.Limit{
				Input: relalg.Sort{
					Input:   relalg.Select{Input: scan(schema), Predicate: pred},
					OrderBy: []relalg.OrderTerm{{Field: "id", Dir: genDir(rng)}},
				},
				Count: int64(1 + rng.Intn(12)), Offset: int64(rng.Intn(4)),
			}}
		case 2:
			c = Case{Expr: relalg.Aggregate{
				Input: relalg.Select{Input: scan(schema), Predicate: pred},
				Aggregates: []relalg.AggCall{
					{Func: relalg.AggCount},
					{Func: relalg.AggSum, Field: "val"},
					{Func: relalg.AggMin, Field: "val"},
					{Func: relalg.AggMax, Field: "val"},
				}}}
		default:
			c = Case{Expr: relalg.Aggregate{
				Input:      relalg.Select{Input: scan(schema), Predicate: pred},
				GroupBy:    []string{"cat"},
				Aggregates: []relalg.AggCall{{Func: relalg.AggCount}, {Func: relalg.AggSum, Field: "val"}},
			}}
		}
		c.Name = fmt.Sprintf("property-%02d", i)
		cases = append(cases, c)
	}
	return cases
}

func genDir(rng *rand.Rand) string {
	if rng.Intn(2) == 0 {
		return "asc"
	}
	return "desc"
}

func genPredicate(rng *rand.Rand, depth int) relalg.Predicate {
	if depth < 2 && rng.Intn(3) == 0 {
		preds := []relalg.Predicate{genPredicate(rng, depth+1), genPredicate(rng, depth+1)}
		if rng.Intn(2) == 0 {
			return relalg.And{Preds: preds}
		}
		return relalg.Or{Preds: preds}
	}
	leaf := genLeaf(rng)
	if rng.Intn(4) == 0 {
		return relalg.Not{P: leaf}
	}
	return leaf
}

func genLeaf(rng *rand.Rand) relalg.Predicate {
	compareOps := []relalg.Op{
		relalg.OpEq, relalg.OpNe, relalg.OpGt, relalg.OpGte, relalg.OpLt, relalg.OpLte,
	}
	switch rng.Intn(5) {
	case 0:
		return cond("id", compareOps[rng.Intn(len(compareOps))], int64(rng.Intn(15)-1))
	case 1:
		vals := []int64{-5, 0, 2, 3, 7, 9, 10, 42}
		return cond("val", compareOps[rng.Intn(len(compareOps))], vals[rng.Intn(len(vals))])
	case 2:
		if rng.Intn(3) == 0 {
			return relalg.IsNull{Field: "cat", Not: rng.Intn(2) == 0}
		}
		cats := []any{"a", "b", "c"}
		if rng.Intn(2) == 0 {
			return cond("cat", relalg.OpEq, cats[rng.Intn(len(cats))])
		}
		return cond("cat", relalg.OpIn, []any{cats[rng.Intn(len(cats))], cats[rng.Intn(len(cats))]})
	case 3:
		prices := []float64{0.25, 2.5, 5}
		return cond("price", compareOps[rng.Intn(len(compareOps))], prices[rng.Intn(len(prices))])
	default:
		patterns := []string{"a%", "%a", "%et%", "_eta", "mu"}
		return cond("name", relalg.OpLike, patterns[rng.Intn(len(patterns))])
	}
}
