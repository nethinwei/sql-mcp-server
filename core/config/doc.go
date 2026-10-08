// Package config defines the runtime configuration model: database, entities,
// per-tool toggles, cost thresholds, cache, rate limits, and audit. It contains
// defaults, validation, encoding-independent presence tracking, and
// presence-aware JSON decoding. Schema exports YAML editor assistance and
// documentation; it is not an encoding/json input contract.
//
// Field rules live in `schema` struct tags (see schema_rules.go) and field
// help in fields.yaml; schema.json and the field reference in
// docs/configuration.md are generated from them.
package config

//go:generate go run ../../internal/schemagen -root ../..
