package config

import (
	"fmt"
	"testing"
)

// largeConfig is 3000 entities of 3 fields each, granted to a role.
func largeConfig() *Config {
	c := validConfig()
	grants := make([]GrantConfig, 0, 3000)
	for i := range 3000 {
		name := fmt.Sprintf("t%04d", i)
		c.Entities = append(c.Entities, EntityConfig{Name: name, DataSource: "main", Schema: "public",
			PrimaryKey: []string{"id"},
			Fields:     []FieldConfig{{Name: "id"}, {Name: "name", Description: "the name"}, {Name: "phone", Mask: "phone"}}})
		grants = append(grants, GrantConfig{Entity: name, Actions: []string{"read"}})
	}
	c.Roles = map[string]RoleDefinition{"analyst": {Grants: grants}}
	return c
}

func BenchmarkValidateLarge(b *testing.B) {
	c := largeConfig()
	if err := c.Validate(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Validate()
	}
}
