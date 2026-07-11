package introspect

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/entity"
)

// Introspector discovers entity metadata (tables, columns, keys, procedure
// parameters) from a live database. Implementations live in x/providers.
type Introspector interface {
	Discover(ctx context.Context, sources []string) ([]entity.Entity, error)
}

// Drift describes differences between configured and discovered schemas.
type Drift struct {
	// Missing lists configured entities/fields absent from the DB
	// (entity name, or "entity.field").
	Missing []string
	// Extra lists DB columns not present in the config.
	Extra []string
	// TypeChanged lists fields whose DB type differs from the config.
	TypeChanged []string
}

// DetectDrift compares configured entities against discovered ones. A missing
// entity is reported by name; missing/extra fields are reported as
// "entity.field". It is pure and deterministic.
func DetectDrift(configured, discovered []entity.Entity) Drift {
	d := Drift{}
	disc := indexByName(discovered)
	for _, ce := range configured {
		physicalName := ce.Source
		if physicalName == "" {
			physicalName = ce.Name
		}
		de, ok := disc[physicalName]
		if !ok {
			d.Missing = append(d.Missing, ce.Name)
			continue
		}
		cf := indexAttrs(ce.Attributes)
		df := indexAttrs(de.Attributes)
		for f := range cf {
			if _, ok := df[f]; !ok {
				d.Missing = append(d.Missing, ce.Name+"."+f)
			}
		}
		for f := range df {
			if _, ok := cf[f]; !ok {
				d.Extra = append(d.Extra, ce.Name+"."+f)
			}
		}
	}
	return d
}

func indexByName(es []entity.Entity) map[string]entity.Entity {
	m := make(map[string]entity.Entity, len(es))
	for _, e := range es {
		m[e.Name] = e
	}
	return m
}

func indexAttrs(as []entity.Attribute) map[string]struct{} {
	m := make(map[string]struct{}, len(as))
	for _, a := range as {
		m[a.Name] = struct{}{}
	}
	return m
}
