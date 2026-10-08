package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPasswordHashing(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("correct horse battery")
	if err != nil || !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("hash = %q, %v", hash, err)
	}
	other, _ := HashPassword("correct horse battery")
	if other == hash {
		t.Fatal("salts must differ")
	}
	if !VerifyPassword(hash, "correct horse battery") {
		t.Fatal("correct password rejected")
	}
	for _, bad := range []string{"wrong", "", "correct horse battery "} {
		if VerifyPassword(hash, bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
	for _, malformed := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$AA$AA", "$argon2id$v=19$x$AA$AA"} {
		if VerifyPassword(malformed, "x") {
			t.Fatalf("malformed hash %q accepted", malformed)
		}
	}
}

func TestValidation(t *testing.T) {
	t.Parallel()
	if err := ValidatePermissions([]string{PermRead, PermWrite}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{"admin:root"}, {PermRead, PermRead}} {
		if err := ValidatePermissions(bad); !errors.Is(err, ErrBadPermission) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	for _, name := range []string{"alice", "ops.team-1", "a"} {
		if err := ValidateUsername(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"", "Alice", "-x", "a b", strings.Repeat("a", 65)} {
		if err := ValidateUsername(name); !errors.Is(err, ErrBadUsername) {
			t.Errorf("%q accepted", name)
		}
	}
	if err := ValidatePassword("short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatal("short password accepted")
	}
	if err := ValidatePassword("十二个字符的中文密码可以吗"); err != nil {
		t.Fatalf("length counts characters, not bytes: %v", err)
	}
}

func TestRequire(t *testing.T) {
	t.Parallel()
	if _, err := Require(context.Background(), PermRead); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("anonymous err = %v", err)
	}
	ctx := WithPrincipal(context.Background(), Principal{Username: "a", Permissions: []string{PermRead}})
	if _, err := Require(ctx, PermRead); err != nil {
		t.Fatal(err)
	}
	if _, err := Require(ctx, PermPublish); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing permission err = %v", err)
	}
	all := WithPrincipal(context.Background(), Principal{Permissions: []string{PermAll}})
	if _, err := Require(all, PermAccounts); err != nil {
		t.Fatalf("admin:* must grant everything: %v", err)
	}
}
