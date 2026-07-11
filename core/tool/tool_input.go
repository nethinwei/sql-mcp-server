package tool

type condJSON struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

type aggJSON struct {
	Func  string `json:"func"`
	Field string `json:"field"`
}

type readInput struct {
	Entity      string         `json:"entity"`
	Transaction string         `json:"transaction,omitempty"`
	Fields      []string       `json:"fields,omitempty"`
	Expand      []string       `json:"expand,omitempty"`
	Filter      []condJSON     `json:"filter,omitempty"`
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
	Entity      string     `json:"entity"`
	Transaction string     `json:"transaction,omitempty"`
	GroupBy     []string   `json:"groupBy,omitempty"`
	Aggregates  []aggJSON  `json:"aggregates"`
	Filter      []condJSON `json:"filter,omitempty"`
}

type executeInput struct {
	Entity      string         `json:"entity"`
	Args        map[string]any `json:"args,omitempty"`
	Transaction string         `json:"transaction,omitempty"`
}
