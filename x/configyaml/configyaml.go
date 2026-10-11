// Package configyaml decodes YAML runtime configuration.
package configyaml

import (
	"bytes"
	"errors"
	"io"
	"os"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"gopkg.in/yaml.v3"
)

// Decode decodes a YAML configuration and resolves it: what it says, with
// its defaults filled in, validated.
func Decode(data []byte) (*config.Config, error) {
	spec, err := DecodeSpec(data)
	if err != nil {
		return nil, err
	}
	return config.Resolve(spec)
}

// DecodeSpec decodes YAML into what it says, strictly (an unknown key fails)
// and canonically (see config.Spec.Canonical), without defaults or
// validation (see config.Resolve).
func DecodeSpec(data []byte) (*config.Spec, error) {
	var spec config.Spec
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&spec); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	spec.Canonical()
	return &spec, nil
}

// EncodeSpec returns the deterministic encoding of spec, what it sets only:
// the export encoding, and the payload of configuration store revisions.
func EncodeSpec(spec *config.Spec) ([]byte, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(spec); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Load reads and decodes a YAML configuration file.
func Load(path string) (*config.Config, error) {
	_, cfg, err := LoadSpec(path)
	return cfg, err
}

// LoadSpec reads a YAML configuration file: what it says, and resolved.
func LoadSpec(path string) (*config.Spec, *config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	spec, err := DecodeSpec(data)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Resolve(spec)
	return spec, cfg, err
}

// Encode returns the encoding of a configuration in effect, every value
// written out: what `export` shows, and the form of payloads stored before
// Specs (they read back as Specs that set every value; see EncodeSpec).
func Encode(cfg *config.Config) ([]byte, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(cfg); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
