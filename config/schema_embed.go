package config

import (
	_ "embed"
	"encoding/json"
)

//go:embed schema.json
var schemaJSON []byte

// Schema returns a JSON Schema for YAML editor assistance and documentation.
// Duration fields therefore describe the YAML string form (for example "30s");
// this schema is not a standard encoding/json input contract. Runtime validation
// and provider capability checks still run when the configuration is loaded.
func Schema() json.RawMessage {
	return schemaJSON
}
