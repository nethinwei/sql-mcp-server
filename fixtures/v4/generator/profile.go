package workload

// relation is a one-level batch-expansion edge exposed in the profile
// (core relationships support exactly one join pair, same datasource).
type relation struct {
	Name, Target, LocalField, TargetField, Cardinality string
}

// entityRelations declares the profile's relationship edges per entity.
var entityRelations = map[string][]relation{
	"applications": {{"organization", "organizations", "organization_id", "id", "belongs-to"}},
	"customers":    {{"organization", "organizations", "organization_id", "id", "belongs-to"}},
	"products":     {{"application", "applications", "application_id", "id", "belongs-to"}},
	"product_price_versions": {
		{"product", "products", "product_id", "id", "belongs-to"},
	},
	"orders": {
		{"application", "applications", "application_id", "id", "belongs-to"},
		{"customer", "customers", "customer_id", "id", "belongs-to"},
	},
	"order_items": {
		{"order", "orders", "order_id", "id", "belongs-to"},
		{"product", "products", "product_id", "id", "belongs-to"},
	},
	"payment_intents": {{"order", "orders", "order_id", "id", "belongs-to"}},
	"payment_attempts": {
		{"intent", "payment_intents", "intent_id", "id", "belongs-to"},
		{"channel", "payment_channels", "channel_id", "id", "belongs-to"},
	},
	"routing_decisions": {
		{"attempt", "payment_attempts", "attempt_id", "id", "belongs-to"},
		{"rule_version", "routing_rule_versions", "rule_version_id", "id", "belongs-to"},
	},
	"refunds":           {{"intent", "payment_intents", "intent_id", "id", "belongs-to"}},
	"channel_callbacks": {{"attempt", "payment_attempts", "attempt_id", "id", "belongs-to"}},
	"fee_rule_versions": {{"channel", "payment_channels", "channel_id", "id", "belongs-to"}},
	"ledger_entries": {
		{"transaction", "ledger_transactions", "transaction_id", "id", "belongs-to"},
		{"account", "ledger_accounts", "account_id", "id", "belongs-to"},
	},
	"balance_snapshots": {{"account", "ledger_accounts", "account_id", "id", "belongs-to"}},
	"settlements":       {{"organization", "organizations", "organization_id", "id", "belongs-to"}},
	"external_statement_items": {
		{"channel", "payment_channels", "channel_id", "id", "belongs-to"},
	},
	"reconciliation_results": {
		{"intent", "payment_intents", "intent_id", "id", "belongs-to"},
	},
	"creators":      {{"agency", "agencies", "agency_id", "id", "belongs-to"}},
	"live_rooms":    {{"creator", "creators", "creator_id", "id", "belongs-to"}},
	"live_sessions": {{"room", "live_rooms", "room_id", "id", "belongs-to"}},
	"gift_price_versions": {
		{"gift", "gift_definitions", "gift_id", "id", "belongs-to"},
	},
	"gift_events": {
		{"session", "live_sessions", "session_id", "id", "belongs-to"},
		{"gift", "gift_definitions", "gift_id", "id", "belongs-to"},
		{"creator", "creators", "creator_id", "id", "belongs-to"},
	},
	"revenue_splits": {
		{"gift_event", "gift_events", "gift_event_id", "id", "belongs-to"},
		{"creator", "creators", "creator_id", "id", "belongs-to"},
	},
	"creator_settlements": {{"creator", "creators", "creator_id", "id", "belongs-to"}},
}

// maskedFields carries the profile's masking examples.
var maskedFields = map[string]string{
	"customers.email": "email",
	"users.email":     "email",
}

// Profile renders the runnable combined-profile server configuration for
// the whole dataset (PostgreSQL datasource, analyst role, all entities).
// Governance stays deliberately light: masks demonstrate field governance
// without changing task answers; row-level and denial scenarios remain the
// regression track's job.
func (d *Dataset) Profile() map[string]any {
	entities := make([]any, 0, len(d.Tables))
	for _, t := range d.Tables {
		entities = append(entities, entityConfig(t))
	}
	return profileConfig(entities)
}

func entityConfig(t *Table) map[string]any {
	fields := make([]any, 0, len(t.Columns))
	for _, c := range t.Columns {
		f := map[string]any{"name": c.Name, "description": c.Description}
		if rule, ok := maskedFields[t.Entity+"."+c.Name]; ok {
			f["mask"] = rule
		}
		fields = append(fields, f)
	}
	e := map[string]any{
		"name":        t.Entity,
		"source":      t.Name,
		"datasource":  "primary",
		"schema":      "public",
		"kind":        "table",
		"description": t.Description,
		"primaryKey":  t.PrimaryKey,
		"roles":       map[string]any{"read": []string{"analyst"}, "aggregate": []string{"analyst"}},
		"fields":      fields,
	}
	if rels := entityRelations[t.Entity]; len(rels) > 0 {
		out := make([]any, 0, len(rels))
		for _, r := range rels {
			out = append(out, map[string]any{
				"name":        r.Name,
				"target":      r.Target,
				"cardinality": r.Cardinality,
				"joinOn":      map[string]string{r.LocalField: r.TargetField},
			})
		}
		e["relationships"] = out
	}
	return e
}

func profileConfig(entities []any) map[string]any {
	return map[string]any{
		"version": "1",
		"server":  map[string]any{"transport": "stdio", "role": "analyst"},
		"databases": map[string]any{
			"primary": map[string]any{"driver": "postgres", "dsn": "${DATABASE_DSN}"},
		},
		"tools": map[string]any{
			"describeEntities": true, "readRecords": true, "aggregateRecords": true,
		},
		// Permissive scan-shape scoring, mirroring the regression profile:
		// v4 grades semantic correctness; the denial repair path still comes
		// from the aggregate predicate guard and the row budget.
		"cost": map[string]any{
			"enabled": true, "softScore": 20, "hardScore": 10,
			"maxRows": 5000, "maxBytes": 16777216,
			"rejectFullScan": false, "whitelistPKPoint": true,
			"queryTimeout": "10s",
		},
		"budget": map[string]any{
			"roles": map[string]any{
				"analyst": map[string]any{
					"maxConcurrent": 16, "maxExecution": "10s",
					"maxReturnedRows": 200, "maxReturnedBytes": 1048576,
					"maxSessionCost": 10000000,
				},
			},
		},
		"cache":    map[string]any{"enabled": false},
		"audit":    map[string]any{"enabled": false},
		"entities": entities,
	}
}
