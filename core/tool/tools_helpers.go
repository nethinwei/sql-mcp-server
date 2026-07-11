package tool

import (
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

func relationshipByName(e entity.Entity, name string) (entity.Relationship, bool) {
	for _, relation := range e.Relations {
		if relation.Name == name {
			return relation, true
		}
	}
	return entity.Relationship{}, false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func effectiveMaxIN(tc Context) int {
	if tc.MaxINListSize > 0 {
		return tc.MaxINListSize
	}
	return relalg.DefaultMaxINCardinality
}
