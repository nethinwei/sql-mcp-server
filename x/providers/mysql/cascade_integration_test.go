//go:build integration

package mysql_test

import (
	"context"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/entity"
)

func TestMySQLCascades(t *testing.T) {
	prov, cleanup := setupMySQL(t)
	defer cleanup()
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE parents (tenant int, id int, PRIMARY KEY (tenant, id))`,
		`CREATE TABLE kids (id int AUTO_INCREMENT PRIMARY KEY, tenant int, parent_id int,
		   FOREIGN KEY (tenant, parent_id) REFERENCES parents (tenant, id) ON DELETE CASCADE ON UPDATE SET NULL)`,
	} {
		if _, err := prov.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	found, err := prov.Introspector().Discover(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]entity.Entity{}
	for _, e := range found {
		byName[e.Name] = e
	}
	cascades := byName["parents"].Cascades
	if len(cascades) != 1 || cascades[0].Table != "kids" || cascades[0].OnDelete != entity.FKCascade ||
		cascades[0].OnUpdate != entity.FKSetNull || !slices.Equal(cascades[0].Columns, []string{"tenant", "id"}) ||
		!slices.Equal(cascades[0].ForeignColumns, []string{"tenant", "parent_id"}) {
		t.Fatalf("parents cascades = %+v", cascades)
	}
	// Creating a trigger needs SUPER on the test server, so trigger detection
	// is covered on PostgreSQL only.
	if byName["parents"].SideEffects {
		t.Fatal("parents has no triggers")
	}
}
