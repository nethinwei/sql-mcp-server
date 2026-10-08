//go:build integration

package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/core/revision/revisiontest"
)

// runServerConformance runs the conformance suite on one database; each
// subtest drops the store tables and initializes them again.
func runServerConformance(t *testing.T, spec Spec) {
	t.Helper()
	revisiontest.Run(t, func(t *testing.T) revision.Store {
		ctx := context.Background()
		db, err := sql.Open(dialects[spec.Driver].driver, spec.DSN)
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"smcp_revisions", "smcp_store_meta", "smcp_admin_accounts"} {
			if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
				t.Fatal(err)
			}
		}
		_ = db.Close()
		if err := Init(ctx, spec, nil); err != nil {
			t.Fatal(err)
		}
		store, err := Open(ctx, spec, nil)
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

func TestPostgresStoreConformance(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("store"), tcpostgres.WithUsername("test"), tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	runServerConformance(t, Spec{Driver: "postgres", DSN: dsn})
}

func TestMySQLStoreConformance(t *testing.T) {
	ctx := context.Background()
	container, err := tcmysql.Run(ctx, "mysql:8",
		tcmysql.WithDatabase("store"), tcmysql.WithUsername("test"), tcmysql.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start mysql container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitForSQL(t, "mysql", dsn)
	runServerConformance(t, Spec{Driver: "mysql", DSN: dsn})
}

func TestOceanBaseStoreConformance(t *testing.T) {
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "oceanbase/oceanbase-ce:4.3.5.6-106000012026040916",
			ExposedPorts: []string{"2881/tcp"},
			WaitingFor:   wait.ForListeningPort("2881/tcp").WithStartupTimeout(5 * time.Minute),
			Env:          map[string]string{"MODE": "mini"},
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start oceanbase container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "2881")
	root := fmt.Sprintf("root:@tcp(%s:%s)/", host, port.Port())
	db := waitForSQL(t, "mysql", root)
	for i := 0; ; i++ {
		if _, err = db.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS store"); err == nil || i == 40 {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		t.Fatalf("create store database: %v", err)
	}
	runServerConformance(t, Spec{Driver: "oceanbase", DSN: root + "store"})
}

// waitForSQL pings until the database accepts connections.
func waitForSQL(t *testing.T, driver, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := 0; i < 60; i++ {
		if err = db.PingContext(context.Background()); err == nil {
			return db
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("database not ready: %v", err)
	return nil
}
