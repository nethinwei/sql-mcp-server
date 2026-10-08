package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/accounts"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
)

type fixture struct {
	h        *Handler
	accounts *accounts.MemoryStore
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{now: time.Unix(1_700_000_000, 0), accounts: accounts.NewMemoryStore(
		accounts.Account{Username: "root", PasswordHash: hash, Permissions: []string{auth.PermAll}},
	)}
	f.h = New(Config{
		Store: revision.NewMemoryStore(nil), Accounts: f.accounts, Now: func() time.Time { return f.now },
	})
	return f
}

func (f *fixture) do(
	method, path, body string,
	headers map[string]string,
	cookie *http.Cookie,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) login(t *testing.T, password string) (*httptest.ResponseRecorder, *http.Cookie, string) {
	t.Helper()
	rec := f.do(http.MethodPost, "/admin/login", `{"username":"root","password":"`+password+`"}`, nil, nil)
	var body struct{ CSRFToken string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return rec, c, body.CSRFToken
		}
	}
	return rec, nil, body.CSRFToken
}

const meQuery = `{"query":"{ me { username } }"}`

func TestLoginSessionAndCSRF(t *testing.T) {
	f := newFixture(t)
	rec, cookie, csrf := f.login(t, "correct horse battery")
	if rec.Code != http.StatusOK || cookie == nil || csrf == "" {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/admin" {
		t.Fatalf("cookie attributes = %+v", cookie)
	}
	if rec := f.do(http.MethodPost, "/admin/graphql", meQuery, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/admin/graphql", meQuery, nil, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF token: %d", rec.Code)
	}
	bad := map[string]string{csrfHeader: "wrong"}
	if rec := f.do(http.MethodPost, "/admin/graphql", meQuery, bad, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong CSRF token: %d", rec.Code)
	}
	ok := map[string]string{csrfHeader: csrf}
	rec = f.do(http.MethodPost, "/admin/graphql", meQuery, ok, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"root"`) {
		t.Fatalf("authenticated query = %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet, "/admin/graphql?query={me{username}}", "", ok, cookie)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GraphQL over GET must be refused: %d", rec.Code)
	}
	rec = f.do(http.MethodGet, "/admin/session", "", nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), csrf) {
		t.Fatalf("session must return the CSRF token for a reloaded console: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, "/admin/logout", "", ok, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/admin/graphql", meQuery, ok, cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("session must end at logout: %d", rec.Code)
	}
}

func TestLoginFailuresLockOut(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < maxLoginFailures; i++ {
		if rec, _, _ := f.login(t, "wrong password!!"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, rec.Code)
		}
	}
	if rec, _, _ := f.login(t, "correct horse battery"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked account must be refused even with the right password: %d", rec.Code)
	}
	f.now = f.now.Add(loginLockout + time.Second)
	if rec, _, _ := f.login(t, "correct horse battery"); rec.Code != http.StatusOK {
		t.Fatalf("lockout must expire: %d", rec.Code)
	}
	rec := f.do(http.MethodPost, "/admin/login", `{"username":"ghost","password":"whatever it is"}`, nil, nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid username or password") {
		t.Fatalf("unknown account must look like a wrong password: %d %s", rec.Code, rec.Body)
	}
}

func TestDisabledAccountLosesSessionAndCannotSignIn(t *testing.T) {
	f := newFixture(t)
	_, cookie, csrf := f.login(t, "correct horse battery")
	disabled := true
	// root is the only account manager; add another so it can be disabled.
	svc := accounts.Service{Store: f.accounts}
	ops := []string{auth.PermAccounts}
	if _, err := svc.Create(context.Background(), "ops", "another long password", ops); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(context.Background(), "root", accounts.Patch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	rec := f.do(http.MethodPost, "/admin/graphql", meQuery, map[string]string{csrfHeader: csrf}, cookie)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled account kept its session: %d", rec.Code)
	}
	if rec, _, _ := f.login(t, "correct horse battery"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled account signed in: %d", rec.Code)
	}
}

func TestPasswordChangeEndsSessions(t *testing.T) {
	f := newFixture(t)
	_, cookie, csrf := f.login(t, "correct horse battery")
	change := `{"query":"mutation { setAdminPassword(input: {username: \"root\", password: \"a brand new passphrase\"}) ` +
		`{ username } }"}`
	headers := map[string]string{csrfHeader: csrf}
	if rec := f.do(http.MethodPost, "/admin/graphql", change, headers, cookie); rec.Code != 200 ||
		strings.Contains(rec.Body.String(), "errors") {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body)
	}
	rec := f.do(http.MethodPost, "/admin/graphql", meQuery, map[string]string{csrfHeader: csrf}, cookie)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a session from before the password change still works: %d", rec.Code)
	}
	if rec, _, _ := f.login(t, "a brand new passphrase"); rec.Code != http.StatusOK {
		t.Fatalf("new password rejected: %d", rec.Code)
	}
}

func TestLoginGateRejectsExcessAttempts(t *testing.T) {
	f := newFixture(t)
	for range maxConcurrentLogins {
		f.h.logins.slots <- struct{}{}
	}
	f.h.logins.queued.Store(maxQueuedLogins)
	start := time.Now()
	rec, _, _ := f.login(t, "correct horse battery")
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), errBusy.Error()) {
		t.Fatalf("excess sign-in = %d %s", rec.Code, rec.Body)
	}
	if time.Since(start) > time.Second {
		t.Fatal("an excess sign-in must be rejected without waiting")
	}
	// A queued attempt proceeds once a slot frees up.
	f.h.logins.queued.Store(0)
	go func() {
		time.Sleep(50 * time.Millisecond)
		f.h.logins.leave()
	}()
	if rec, _, _ := f.login(t, "correct horse battery"); rec.Code != http.StatusOK {
		t.Fatalf("queued sign-in = %d", rec.Code)
	}
}

func TestSessionsExpire(t *testing.T) {
	f := newFixture(t)
	_, cookie, csrf := f.login(t, "correct horse battery")
	f.now = f.now.Add(sessionIdleTimeout + time.Minute)
	rec := f.do(http.MethodPost, "/admin/graphql", meQuery, map[string]string{csrfHeader: csrf}, cookie)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("idle session must expire: %d", rec.Code)
	}
}

func TestSelectionDepth(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		`{ a }`:             1,
		`{ a { b { c } } }`: 3,
		`{ a { ...F } } fragment F on T { b { c { d } } }`: 4,
		`{ a { ... on T { b } } }`:                         2,
	}
	for query, want := range cases {
		doc, err := parser.ParseQuery(&ast.Source{Input: query})
		if err != nil {
			t.Fatal(err)
		}
		if got := selectionDepth(doc.Operations[0].SelectionSet, doc.Fragments, 0); got != want {
			t.Errorf("%s: depth %d, want %d", query, got, want)
		}
	}
	var deep strings.Builder
	for i := 0; i <= maxQueryDepth; i++ {
		deep.WriteString("{ a ")
	}
	deep.WriteString(strings.Repeat("}", maxQueryDepth+1))
	doc, err := parser.ParseQuery(&ast.Source{Input: deep.String()})
	if err != nil {
		t.Fatal(err)
	}
	rc := &graphql.OperationContext{Operation: doc.Operations[0], Doc: doc}
	if gqlErr := depthLimit(maxQueryDepth).MutateOperationContext(context.Background(), rc); gqlErr == nil {
		t.Fatal("an operation deeper than the limit must be rejected")
	}
}

func TestPlaygroundIsOffByDefault(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(http.MethodGet, "/admin/playground", "", nil, nil); strings.Contains(rec.Body.String(), "graphiql") {
		t.Fatal("playground must be off by default")
	}
	rec := f.do(http.MethodGet, "/admin/session", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("session without cookie = %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/admin/roles/analyst", "", nil, nil); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("console routes must serve the app with a CSP: %d %v", rec.Code, rec.Header())
	}
}
