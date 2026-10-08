package bootstrap

import (
	"strings"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

func connectionsConfig(entity config.EntityConfig) *config.Config {
	cfg := &config.Config{
		Databases: map[string]config.DatabaseConfig{"main": {
			Driver: "postgres",
			Connections: config.Connections{
				"rw": {DSN: "x"}, "ro": {DSN: "y"}, "replica": {DSN: "z", Role: "replica", Pooler: "transaction"},
			},
			Routing:        config.RoutingConfig{Read: "replica", Write: "rw", Execute: "ro"},
			ReadAfterWrite: 1e9,
		}},
		Entities: []config.EntityConfig{entity},
	}
	cfg.ApplyDefaults()
	return cfg
}

func TestAssembleRoutesEachActionToItsConnection(t *testing.T) {
	t.Parallel()
	users := entity.Entity{Name: "users", Source: "users"}
	conns := Connections{"main": {
		"rw": relationProvider("srv", false, users), "ro": relationProvider("srv", false, users),
		"replica": relationProvider("srv", false, users),
	}}
	cfg := connectionsConfig(
		config.EntityConfig{Name: "users", DataSource: "main", Fields: []config.FieldConfig{{Name: "id"}}})
	app, err := AssembleWithConnections(cfg, conns)
	if err != nil {
		t.Fatal(err)
	}
	source := app.Sources["main"]
	inner := func(db store.DB) store.DB { return db.(*store.PreparedDB).DB }
	if inner(source.DB) != conns["main"]["replica"] || inner(source.Write) != conns["main"]["rw"] ||
		inner(source.Execute) != conns["main"]["ro"] {
		t.Fatal("reads, writes and procedure calls must use their routed connections")
	}
	if source.ReadTx != conns["main"]["replica"] || app.TxBeginners["main"] != conns["main"]["rw"] {
		t.Fatal("read-only transactions begin on the read connection, read-write ones on the write connection")
	}
	if source.ReadAfterWrite != 1e9 || app.Providers["main"] != conns["main"]["replica"] {
		t.Fatalf("read-after-write = %v, introspection provider = %v", source.ReadAfterWrite, app.Providers["main"])
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	for name, p := range conns["main"] {
		if p.(*catalogProvider).closed != 1 {
			t.Errorf("connection %s closed %d times", name, p.(*catalogProvider).closed)
		}
	}
}

func TestAssembleRejectsConnectionsWithDifferentDefaultSchemas(t *testing.T) {
	t.Parallel()
	users := entity.Entity{Name: "users", Source: "users"}
	other := relationProvider("srv", false, users)
	other.defaultSchema = "app"
	conns := Connections{"main": {
		"rw": relationProvider("srv", false, users), "ro": relationProvider("srv", false, users), "replica": other,
	}}
	unqualified := config.EntityConfig{Name: "users", DataSource: "main", Fields: []config.FieldConfig{{Name: "id"}}}
	if _, err := AssembleWithConnections(connectionsConfig(unqualified), conns); err == nil ||
		!strings.Contains(err.Error(), "different schemas") {
		t.Fatalf("error = %v, want a default schema mismatch", err)
	}
	qualified := unqualified
	qualified.Schema = "public"
	if _, err := AssembleWithConnections(connectionsConfig(qualified), conns); err != nil {
		t.Fatalf("entities naming their schema need no common default: %v", err)
	}
}
