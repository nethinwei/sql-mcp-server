// Package config defines the runtime configuration model: database, entities,
// per-tool toggles, cost thresholds, cache, rate limits, and audit. It is pure
// structure plus defaults and validation — no YAML parsing here (that lives in
// x/bootstrap, which depends on gopkg.in/yaml.v3). Schema exports a JSON Schema
// for IDE validation and documentation.
package config
