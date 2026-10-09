package main

import (
	"path/filepath"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
)

// A configuration whose certificate cannot be read is not prepared, so the
// reload building it fails before anything is published.
func TestPreparedAuthsFailOnUnreadableCertificate(t *testing.T) {
	t.Parallel()
	auths := &preparedAuths{http: true, addr: "127.0.0.1:0"}
	cfg := &config.Config{}
	cfg.Server.Auth.Token = "t"
	missing := filepath.Join(t.TempDir(), "missing.pem")
	cfg.Server.Auth.TLS.Cert, cfg.Server.Auth.TLS.Key = missing, missing
	if _, err := auths.prepare(cfg); err == nil {
		t.Fatal("preparing an unreadable certificate must fail the reload")
	}
}

// Publishing an App switches to the authentication prepared for it, once;
// what was prepared for Apps never published is dropped.
func TestPreparedAuthsFollowPublishedApps(t *testing.T) {
	t.Parallel()
	next, unpublished := &bootstrap.App{}, &bootstrap.App{}
	runtime := bootstrap.NewRuntimeWithBuilder(&bootstrap.App{}, func(string) (*bootstrap.App, error) { return next, nil })
	defer func() { _ = runtime.Close() }()
	auths := &preparedAuths{http: true, addr: "127.0.0.1:0"}
	cfg := &config.Config{}
	cfg.Server.Auth.Token = "new"
	prepared, err := auths.prepare(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	auths.follow(runtime)(func(mcpserver.PreparedAuth) error { applied++; return nil })
	auths.keep(unpublished, prepared)
	auths.keep(next, prepared)
	if err := runtime.Reload("ignored"); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("switched %d times, want once", applied)
	}
	if _, ok := auths.byApp.Load(unpublished); ok {
		t.Fatal("an authentication prepared for an App never published must be dropped")
	}
}
