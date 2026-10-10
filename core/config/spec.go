package config

import "reflect"

//go:generate go run ../../internal/schemagen -root ../..

// A configuration is held two ways. Spec is what its author wrote: a field
// that was not written is nil, so a payload records exactly the intent, and
// an unset field takes whatever its default is when the configuration loads.
// Config is the configuration in effect: Resolve fills the defaults in and
// validates it, and serving reads nothing else. Spec is generated from
// Config (spec_gen.go), so a field is added to Config, with its tags, and
// `go generate ./core/config` writes the rest.

// List is a list of a Spec: nil when unset, and kept when set empty, so an
// empty list written stays written.
type List[T any] []T

// IsZero reports an unset list (omitted when encoded), not an empty one.
func (l List[T]) IsZero() bool { return l == nil }

// Map is a map of a Spec: nil when unset, and kept when set empty.
type Map[K comparable, V any] map[K]V

// IsZero reports an unset map (omitted when encoded), not an empty one.
func (m Map[K, V]) IsZero() bool { return m == nil }

// Resolve is the configuration spec describes: its defaults filled in, and
// validated.
func Resolve(spec *Spec) (*Config, error) {
	var cfg Config
	if spec != nil {
		cfg = spec.config()
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func ptr[T any](v T) *T { return &v }

// set is v when it is not zero, else nil: a field of a configuration built in
// code, where zero means unset (see SpecOf).
func set[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// isZero reports a Spec with no field set.
func isZero[T any](v T) bool { return reflect.ValueOf(v).IsZero() }

// Canonical unsets what spec writes but means unset: a zero where the
// configuration omits zero (older encodings wrote some, such as an empty
// schema). Decoding canonicalizes, so two payloads that say the same read
// the same.
func (s *Spec) Canonical() { s.canonical() }

// SpecOf is the Spec of a configuration built in code, where a zero field
// means unset (as before ApplyDefaults), or of one in effect, its defaults
// then written out.
func SpecOf(c *Config) *Spec {
	s := c.spec()
	return &s
}
