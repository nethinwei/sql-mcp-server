package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

type fakeUsers map[string]bootstrap.UserIdentity // keyed by user name; token = name + "-token"

func (f fakeUsers) UserByTokenHash(hash string) (bootstrap.UserIdentity, bool) {
	for name, id := range f {
		if config.TokenHash(name+"-token") == hash {
			return id, true
		}
	}
	return bootstrap.UserIdentity{}, false
}

func (f fakeUsers) UserByName(name string) (bootstrap.UserIdentity, bool) {
	id, ok := f[name]
	return id, ok
}

func testUsers() fakeUsers {
	return fakeUsers{"alice": {Principal: "user:alice", Subject: map[string]any{"tenant_id": "t1"}}}
}

// identityProbe records the identity the handler chain produced.
func identityProbe(got *requestSubject) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, _ := r.Context().Value(subjectCtxKey{}).(requestSubject)
		*got = s
		w.WriteHeader(http.StatusOK)
	})
}

func serveWith(h http.Handler, headers map[string]string) int {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestPrincipalAuth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		token         string
		channelAuthed bool
		authz         string
		wantCode      int
		wantRole      string
	}{
		{"user token", "shared", false, "Bearer alice-token", http.StatusOK, "user:alice"},
		{"user token without shared token", "", false, "Bearer alice-token", http.StatusOK, "user:alice"},
		{"shared token keeps default identity", "shared", false, "Bearer shared", http.StatusOK, ""},
		{"unknown token", "shared", false, "Bearer nope", http.StatusUnauthorized, ""},
		{"users require a token", "", false, "", http.StatusUnauthorized, ""},
		{"unknown token without shared token", "", true, "Bearer nope", http.StatusUnauthorized, ""},
		{"authenticated channel may omit token", "", true, "", http.StatusOK, ""},
		{"authenticated channel does not bypass shared token", "shared", true, "", http.StatusUnauthorized, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got requestSubject
			h := principalAuth(tc.token, testUsers(), tc.channelAuthed, identityProbe(&got))
			code := serveWith(h, map[string]string{"Authorization": tc.authz})
			if code != tc.wantCode || got.role != tc.wantRole {
				t.Fatalf("code=%d role=%q, want %d %q", code, got.role, tc.wantCode, tc.wantRole)
			}
			if tc.wantRole != "" && (!got.user || got.attrs["tenant_id"] != "t1") {
				t.Fatalf("user identity must carry configured subject: %+v", got)
			}
		})
	}
}

func TestWithProxyIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		headers  map[string]string
		wantCode int
		wantRole string
		wantAttr map[string]any
	}{
		{"user header", map[string]string{"X-MCP-User": " Alice "}, http.StatusOK, "user:alice",
			map[string]any{"tenant_id": "t1"}},
		{"configured tenant wins over header", map[string]string{
			"X-MCP-User": "alice", "X-MCP-Subject": `{"tenant_id":"t9","region":"CN"}`,
		}, http.StatusOK, "user:alice", map[string]any{"tenant_id": "t1", "region": "CN"}},
		{"unknown user", map[string]string{"X-MCP-User": "mallory"}, http.StatusForbidden, "", nil},
		{"user with role", map[string]string{"X-MCP-User": "alice", "X-MCP-Role": "admin"},
			http.StatusBadRequest, "", nil},
		{"legacy role header", map[string]string{"X-MCP-Role": "Reader"}, http.StatusOK, "reader", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got requestSubject
			code := serveWith(withProxyIdentity(testUsers(), identityProbe(&got)), tc.headers)
			if code != tc.wantCode || got.role != tc.wantRole {
				t.Fatalf("code=%d role=%q, want %d %q", code, got.role, tc.wantCode, tc.wantRole)
			}
			if tc.wantAttr != nil && !reflect.DeepEqual(got.attrs, tc.wantAttr) {
				t.Fatalf("attrs = %v, want %v", got.attrs, tc.wantAttr)
			}
		})
	}
}

func TestBearerUserCannotBeOverriddenByProxyHeaders(t *testing.T) {
	t.Parallel()
	var got requestSubject
	h := principalAuth("", testUsers(), true, withProxyIdentity(testUsers(), identityProbe(&got)))
	for _, header := range []string{"X-MCP-User", "X-MCP-Role", "X-MCP-Subject"} {
		code := serveWith(h, map[string]string{"Authorization": "Bearer alice-token", header: `{"x":1}`})
		if code != http.StatusForbidden {
			t.Fatalf("%s with a bearer user: code=%d, want 403", header, code)
		}
	}
	if code := serveWith(h, map[string]string{"Authorization": "Bearer alice-token"}); code != http.StatusOK ||
		got.role != "user:alice" {
		t.Fatalf("plain bearer user: code=%d identity=%+v", code, got)
	}
}

func TestCallerIdentityUsesDefaultUser(t *testing.T) {
	t.Parallel()
	app := &bootstrap.App{
		DefaultRole: "reader", DefaultUser: "alice",
		Users: map[string]bootstrap.UserIdentity{"alice": testUsers()["alice"]},
	}
	role, subject := callerIdentity(t.Context(), app)
	if role != "user:alice" || subject["tenant_id"] != "t1" {
		t.Fatalf("default user identity = %q %v", role, subject)
	}
	role, _ = callerIdentity(WithSubject(t.Context(), "Operator", nil), app)
	if role != "operator" {
		t.Fatalf("explicit identity must win over the default user: %q", role)
	}
	app.Users = nil
	role, subject = callerIdentity(t.Context(), app)
	if role != "user:alice" || subject != nil {
		t.Fatalf("removed default user must fail closed to a grant-less principal: %q %v", role, subject)
	}
	app.DefaultUser = ""
	if role, _ = callerIdentity(t.Context(), app); role != "reader" {
		t.Fatalf("without default user the default role applies: %q", role)
	}
}

func TestRevokeSessionsDropsBindingsAndRollsBack(t *testing.T) {
	t.Parallel()
	store := newSessionIdentityStore()
	store.bind("s1", sessionIdentity{role: "user:alice"})
	store.bind("s2", sessionIdentity{role: "user:alice", subject: `{"tenant_id":"t1"}`})
	store.bind("s3", sessionIdentity{role: "user:bob"})
	var closed []string
	returned := revokeSessions(store, func(s string) { closed = append(closed, s) })([]string{"user:alice"})
	slices.Sort(closed)
	slices.Sort(returned)
	if !slices.Equal(closed, []string{"s1", "s2"}) || !slices.Equal(returned, closed) {
		t.Fatalf("closed sessions = %v", closed)
	}
	if store.matches("s1", sessionIdentity{role: "user:alice"}) {
		t.Fatal("revoked session must no longer match")
	}
	if !store.matches("s3", sessionIdentity{role: "user:bob"}) {
		t.Fatal("other users' sessions must be kept")
	}
}

func TestValidateHTTPSecurityAcceptsUsersAsAuthentication(t *testing.T) {
	t.Parallel()
	if err := validateHTTPSecurity(HTTPConfig{Addr: ":8080"}); err == nil {
		t.Fatal("exposed listener without auth must be rejected")
	}
	if err := validateHTTPSecurity(HTTPConfig{Addr: ":8080", Users: testUsers()}); err != nil {
		t.Fatalf("configured users authenticate an exposed listener: %v", err)
	}
}
