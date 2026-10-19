package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
)

// usersApp serves the users named, each with the token of its name.
func usersApp(names ...string) *App {
	app := &App{Users: map[string]UserIdentity{}, UserTokens: map[string]string{}}
	for _, name := range names {
		app.Users[name] = UserIdentity{Principal: UserPrincipal(name)}
		app.UserTokens[config.TokenHash(name)] = name
	}
	return app
}

// usersPayload is a store payload serving the users given as name: disabled.
func usersPayload(users map[string]bool) string {
	payload := fmt.Sprintf("databases:\n  main:\n    driver: %s\n    dsn: x\nusers:\n", sharedTestDriver)
	for name, disabled := range users {
		payload += fmt.Sprintf("  %s:\n    tokenHash: %s\n    disabled: %t\n", name, config.TokenHash(name), disabled)
	}
	return payload
}

// A revision that disables a user and fails to apply for another reason (a
// schema drifted) still denies the user at the next poll, drops its
// sessions, and keeps the others; a revision that applies lifts the hold.
func TestRevocationAppliesWhileTheRevisionFails(t *testing.T) {
	t.Parallel()
	store := revision.NewMemoryStore(nil)
	first := publishPayload(t, store, usersPayload(map[string]bool{"alice": false, "bob": false}))
	runtime := NewRuntimeWithBuilder(usersApp("alice", "bob"), nil)
	defer runtime.Close()
	var mu sync.Mutex
	var revoked []string
	runtime.OnRevokedPrincipals(func(p []string) []string {
		mu.Lock()
		defer mu.Unlock()
		revoked = append(revoked, p...)
		return nil
	})
	drifted := errors.New(`entity "orders": column "total" no longer exists`)
	build := func(rev revision.Revision) (*App, error) {
		if rev.ID == first.ID+1 {
			return nil, drifted
		}
		return usersApp("alice", "bob"), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runtime.WatchStore(ctx, store, first.ID, time.Millisecond, build, nil) }()

	failing := publishPayload(t, store, usersPayload(map[string]bool{"alice": false, "bob": true}))
	waitFor(t, func() bool { _, ok := runtime.UserByTokenHash(config.TokenHash("bob")); return !ok })
	if s, ok := runtime.Stale(); !ok || s.RevisionID != failing.ID {
		t.Fatalf("stale = %+v %v", s, ok)
	}
	if _, ok := runtime.UserByName("bob"); ok {
		t.Fatal("a disabled user resolves by name")
	}
	if _, ok := runtime.UserByTokenHash(config.TokenHash("alice")); !ok {
		t.Fatal("a user the revision keeps was held")
	}
	time.Sleep(20 * time.Millisecond) // later polls retry the revision
	mu.Lock()
	if !reflect.DeepEqual(revoked, []string{"user:bob"}) {
		t.Fatalf("revoked = %v, want bob's sessions dropped once", revoked)
	}
	mu.Unlock()

	publishPayload(t, store, usersPayload(map[string]bool{"alice": false, "bob": false}))
	waitFor(t, func() bool { _, ok := runtime.UserByName("bob"); return ok })
}

// A user is held when the revision changes anything that may narrow it.
func TestChangedUsers(t *testing.T) {
	t.Parallel()
	base := func() *config.Config {
		return &config.Config{Users: map[string]config.UserConfig{
			"alice": {TokenHash: config.TokenHash("alice"), Roles: []string{"reader"}},
			"bob":   {TokenHash: config.TokenHash("bob"), Permissions: []string{"read"}},
		}}
	}
	app := &App{config: base()}
	app.Users, app.UserTokens, app.UserCerts = userDirectory(app.config)
	cases := map[string]struct {
		change func(*config.Config)
		held   []string
	}{
		"nothing": {func(*config.Config) {}, nil},
		"removed": {func(c *config.Config) { delete(c.Users, "alice") }, []string{"alice"}},
		"roles":   {setUser("alice", func(u *config.UserConfig) { u.Roles = nil }), []string{"alice"}},
		"token":   {setUser("bob", func(u *config.UserConfig) { u.TokenHash = "x" }), []string{"bob"}},
		"cert":    {setUser("bob", func(u *config.UserConfig) { u.CertSubjects = []string{"cn:bob"} }), []string{"bob"}},
		"perms":   {setUser("bob", func(u *config.UserConfig) { u.Permissions = nil }), []string{"bob"}},
		"text":    {setUser("bob", func(u *config.UserConfig) { u.Description = "x" }), nil},
		"added":   {func(c *config.Config) { c.Users["carol"] = config.UserConfig{} }, nil},
	}
	for name, c := range cases {
		next := base()
		c.change(next)
		var held []string
		for user := range changedUsers(app, next) {
			held = append(held, user)
		}
		if !reflect.DeepEqual(held, c.held) {
			t.Errorf("%s: held %v, want %v", name, held, c.held)
		}
	}
}

// setUser changes a configuration's user.
func setUser(name string, change func(*config.UserConfig)) func(*config.Config) {
	return func(c *config.Config) {
		u := c.Users[name]
		change(&u)
		c.Users[name] = u
	}
}

// A reload that changes a user's subject ends the user's sessions, bound to
// the subject that opened them; one that changes nothing else keeps them.
func TestRevokedPrincipalsIncludeChangedSubjects(t *testing.T) {
	t.Parallel()
	app := func(subject map[string]any) *App {
		return &App{Users: map[string]UserIdentity{
			"alice": {Principal: "user:alice", Subject: subject},
			"bob":   {Principal: "user:bob"},
		}}
	}
	old := app(map[string]any{"tenant_id": 1})
	if got := revokedPrincipals(old, app(map[string]any{"tenant_id": 1})); len(got) != 0 {
		t.Fatalf("unchanged subject revoked %v", got)
	}
	got := revokedPrincipals(old, app(map[string]any{"tenant_id": 7}))
	if !reflect.DeepEqual(got, []string{"user:alice"}) {
		t.Fatalf("changed subject revoked %v", got)
	}
}

// A credential resolves within one snapshot: a reload that rotates alice's
// token never lets the old token reach the identity the new snapshot gives
// her, however the lookup interleaves with the publish.
func TestCredentialResolvesWithinOneSnapshot(t *testing.T) {
	t.Parallel()
	rotated := func(token, principal string) *App {
		return &App{
			Users:      map[string]UserIdentity{"alice": {Principal: principal}},
			UserTokens: map[string]string{config.TokenHash(token): "alice"},
		}
	}
	before, after := rotated("old", "user:alice-before"), rotated("new", "user:alice-after")
	runtime := NewRuntimeWithBuilder(before, nil)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			next := before
			if i%2 == 0 {
				next = after
			}
			runtime.current.Store(newAppSnapshot(next))
		}
	}()
	for range 200000 {
		if id, ok := runtime.UserByTokenHash(config.TokenHash("old")); ok && id.Principal != "user:alice-before" {
			close(stop)
			<-done
			t.Fatalf("the rotated-out token resolved to %q", id.Principal)
		}
	}
	close(stop)
	<-done
}
