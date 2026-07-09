package introspect

import (
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/entity"
)

func TestDetectDriftFieldLevel(t *testing.T) {
	t.Parallel()
	cfg := []entity.Entity{{
		Name:       "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "phone"}},
	}}
	disc := []entity.Entity{{
		Name:       "users",
		Attributes: []entity.Attribute{{Name: "id"}, {Name: "email"}},
	}}
	d := DetectDrift(cfg, disc)
	if !slices.Contains(d.Missing, "users.phone") {
		t.Fatalf("expected users.phone in Missing, got %v", d.Missing)
	}
	if !slices.Contains(d.Extra, "users.email") {
		t.Fatalf("expected users.email in Extra, got %v", d.Extra)
	}
}

func TestDetectDriftMissingEntity(t *testing.T) {
	t.Parallel()
	d := DetectDrift([]entity.Entity{{Name: "ghost"}}, nil)
	if !slices.Contains(d.Missing, "ghost") {
		t.Fatalf("expected ghost in Missing, got %v", d.Missing)
	}
}

func TestDetectDriftNoDrift(t *testing.T) {
	t.Parallel()
	e := []entity.Entity{{Name: "users", Attributes: []entity.Attribute{{Name: "id"}}}}
	d := DetectDrift(e, e)
	if len(d.Missing) != 0 || len(d.Extra) != 0 {
		t.Fatalf("expected no drift, got %+v", d)
	}
}
