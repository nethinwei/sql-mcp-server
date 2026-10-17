package tool

// condJSON is one filter condition, or a group of them: any (OR), all (AND)
// or not; or exists, a relationship some target row of which matches
// Filter.
type condJSON struct {
	Field  string     `json:"field"`
	Op     string     `json:"op"`
	Value  any        `json:"value"`
	Any    []condJSON `json:"any,omitempty"`
	All    []condJSON `json:"all,omitempty"`
	Not    *condJSON  `json:"not,omitempty"`
	Exists string     `json:"exists,omitempty"`
	Filter []condJSON `json:"filter,omitempty"`
}

type aggJSON struct {
	Func     string `json:"func"`
	Field    string `json:"field"`
	Distinct bool   `json:"distinct,omitempty"`
}

// orderJSON is one sort term: a field and asc (the default) or desc.
type orderJSON struct {
	Field string `json:"field"`
	Dir   string `json:"dir,omitempty"`
}

type readInput struct {
	Entity      string         `json:"entity"`
	Transaction string         `json:"transaction,omitempty"`
	Fields      []string       `json:"fields,omitempty"`
	Expand      []string       `json:"expand,omitempty"`
	Filter      []condJSON     `json:"filter,omitempty"`
	OrderBy     []orderJSON    `json:"orderBy,omitempty"`
	Distinct    bool           `json:"distinct,omitempty"`
	Limit       int64          `json:"limit,omitempty"`
	Offset      int64          `json:"offset,omitempty"`
	Cursor      map[string]any `json:"cursor,omitempty"`
}

type createInput struct {
	Entity      string         `json:"entity"`
	Transaction string         `json:"transaction,omitempty"`
	Values      map[string]any `json:"values"`
}

type updateInput struct {
	Entity      string         `json:"entity"`
	Transaction string         `json:"transaction,omitempty"`
	Filter      []condJSON     `json:"filter"`
	Set         map[string]any `json:"set"`
}

type deleteInput struct {
	Entity      string     `json:"entity"`
	Transaction string     `json:"transaction,omitempty"`
	Filter      []condJSON `json:"filter"`
}

type aggregateInput struct {
	Entity      string      `json:"entity"`
	Transaction string      `json:"transaction,omitempty"`
	GroupBy     []string    `json:"groupBy,omitempty"`
	Aggregates  []aggJSON   `json:"aggregates"`
	Filter      []condJSON  `json:"filter,omitempty"`
	Having      []condJSON  `json:"having,omitempty"`
	OrderBy     []orderJSON `json:"orderBy,omitempty"`
	Limit       int64       `json:"limit,omitempty"`
}

type executeInput struct {
	Entity      string         `json:"entity"`
	Args        map[string]any `json:"args,omitempty"`
	Transaction string         `json:"transaction,omitempty"`
}
