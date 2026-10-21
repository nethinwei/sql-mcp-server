package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
)

// The provider serves PostgreSQL 11 to 18 and refuses the rest, including the
// servers that speak its protocol but are not PostgreSQL.
func TestCheckServer(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		num      int
		version  string
		hologres bool
		ok       bool
	}{
		{110000, "PostgreSQL 11.0 on x86_64-pc-linux-gnu", false, true},
		{110022, "PostgreSQL 11.22 on aarch64-unknown-linux-musl", false, true},
		{160014, "PostgreSQL 16.14 on aarch64-unknown-linux-musl, compiled by gcc", false, true},
		{180005, "PostgreSQL 18.5 on x86_64-pc-linux-gnu", false, true},
		{100023, "PostgreSQL 10.23 on x86_64-pc-linux-gnu", false, false},
		{190000, "PostgreSQL 19devel on x86_64-pc-linux-gnu", false, false},
		{110003, "PostgreSQL 11.3 (Release-build@...) on x86_64-pc-linux-gnu, Hologres 3.0", false, false},
		{110003, "PostgreSQL 11.3 on x86_64-pc-linux-gnu", true, false},
		{120012, "PostgreSQL 12.12 (Greenplum Database 7.1.0 build commit:...)", false, false},
		{80002, "PostgreSQL 8.0.2 on i686-pc-linux-gnu, Redshift 1.0.77467", false, false},
		{130000, "CockroachDB CCL v24.1.0 (x86_64-pc-linux-gnu)", false, false},
		{90204, "(openGauss 5.0.0 build a07d57c3) compiled at 2023-03-29", false, false},
		{110002, "PostgreSQL 11.2-YB-2.6.1.0-b0 on x86_64-pc-linux-gnu, compiled by gcc 5.5.0, 64-bit", false, false},
	} {
		err := checkServer(c.num, c.version, c.hologres)
		if c.ok != (err == nil) || err != nil && !errors.Is(err, ErrUnsupportedServer) {
			t.Errorf("%d %q hologres=%v: %v, want ok=%v", c.num, c.version, c.hologres, err, c.ok)
		}
	}
}

// A server that does not answer (an unroutable address) fails the open within
// the connect timeout instead of holding startup.
func TestOpenGivesUpOnAnUnreachableServer(t *testing.T) {
	saved := connectTimeout
	connectTimeout = 300 * time.Millisecond
	defer func() { connectTimeout = saved }()
	start := time.Now()
	_, err := Open("postgres://u:p@192.0.2.1:5432/db?sslmode=disable",
		providerregistry.Options{Timeout: time.Second})
	if !errors.Is(err, ErrPing) {
		t.Fatalf("Open = %v, want ErrPing", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Open took %s, want it bounded by the connect timeout", elapsed)
	}
}

// An explicit connect_timeout, 0 (no limit) too, is the DSN's; the default
// bounds only a connection whose DSN sets none.
func TestConnectTimeoutSet(t *testing.T) {
	for dsn, set := range map[string]bool{
		"postgres://u:p@h/db?connect_timeout=0":                    true,
		"postgresql://u:p@h/db?sslmode=disable":                    false,
		"host=h dbname=db connect_timeout = 0":                     true,
		"host=h dbname=db application_name=x":                      false,
		"host=h dbname=db my_connect_timeout=3":                    false,
		"postgres://u:p@h/db?application_name=connect_timeout%3D1": false,
	} {
		if got := connectTimeoutSet(dsn); got != set {
			t.Errorf("%q: %v, want %v", dsn, got, set)
		}
	}
	t.Setenv("PGCONNECT_TIMEOUT", "0")
	if !connectTimeoutSet("host=h") {
		t.Error("PGCONNECT_TIMEOUT is not counted")
	}
}
