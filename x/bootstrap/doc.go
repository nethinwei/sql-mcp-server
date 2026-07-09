// Package bootstrap loads configuration and assembles the application: it
// resolves secret placeholders, opens the database provider, converts config
// entities to the core entity model, and wires the registry, authorizer, cost
// gate, engine, and tools. It is the only place that imports gopkg.in/yaml.v3
// and x/providers.
package bootstrap
