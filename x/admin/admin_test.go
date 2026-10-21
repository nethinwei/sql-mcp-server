package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

// A cross-site page can post a form or text/plain without a preflight: login
// refuses them, and logout acts only for the session with its CSRF token.
func TestLoginAndLogoutRefuseCrossSiteRequests(t *testing.T) {
	f := newFixture(t)
	body := `{"username":"root","password":"correct horse battery"}`
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data", ""} {
		rec := f.do(http.MethodPost, "/admin/login", body, map[string]string{"Content-Type": ct}, nil)
		if rec.Code != http.StatusUnsupportedMediaType || rec.Header().Get("Set-Cookie") != "" {
			t.Fatalf("login as %q = %d, cookie %q", ct, rec.Code, rec.Header().Get("Set-Cookie"))
		}
	}
	json := map[string]string{"Content-Type": "application/json; charset=utf-8"}
	if rec := f.do(http.MethodPost, "/admin/login", body, json, nil); rec.Code != http.StatusOK {
		t.Fatalf("login with a JSON media type and parameters = %d", rec.Code)
	}
	_, cookie, csrf := f.login(t, "correct horse battery")
	for name, c := range map[string]*http.Cookie{"no session": nil, "stale session": {Name: cookieName, Value: "x"}} {
		rec := f.do(http.MethodPost, "/admin/logout", "", map[string]string{csrfHeader: csrf}, c)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Set-Cookie") != "" {
			t.Fatalf("logout with %s = %d, cookie %q", name, rec.Code, rec.Header().Get("Set-Cookie"))
		}
	}
	rec := f.do(http.MethodPost, "/admin/logout", "", nil, cookie)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("logout without the CSRF token = %d, cookie %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}
	rec = f.do(http.MethodPost, "/admin/graphql", meQuery, map[string]string{csrfHeader: csrf}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("a refused logout must keep the session: %d", rec.Code)
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

// Lockouts in a row double, up to maxLoginLockout; a sign-in, or quiet for
// maxLoginLockout, starts over.
func TestLoginLockoutsInARowDouble(t *testing.T) {
	f := newFixture(t)
	lockOut := func() {
		for i := 0; i < maxLoginFailures; i++ {
			f.login(t, "wrong password!!")
		}
	}
	for _, d := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		lockOut()
		f.now = f.now.Add(d - time.Second)
		if rec, _, _ := f.login(t, "correct horse battery"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("a lockout of %v ended early: %d", d, rec.Code)
		}
		f.now = f.now.Add(2 * time.Second)
	}
	for _, d := range []time.Duration{8, 16, 32, 60, 60} { // minutes: 64 is capped
		lockOut()
		f.now = f.now.Add(d*time.Minute - time.Second)
		if f.h.sessions.allowLogin("root") {
			t.Fatalf("a lockout of %d minutes ended early", d)
		}
		f.now = f.now.Add(2 * time.Second)
		if !f.h.sessions.allowLogin("root") {
			t.Fatalf("a lockout outlasted %d minutes", d)
		}
	}
	f.now = f.now.Add(maxLoginLockout + time.Second)
	lockOut()
	f.now = f.now.Add(loginLockout + time.Second)
	if rec, _, _ := f.login(t, "correct horse battery"); rec.Code != http.StatusOK {
		t.Fatalf("after quiet, a lockout must start over: %d", rec.Code)
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
	change := `{"query":"mutation { setAdminPassword(input: {username: \"root\", password: \"a brand new passphrase\", ` +
		`currentPassword: \"correct horse battery\"}) { username } }"}`
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

// An operation above the complexity limit is refused before any resolver
// runs, however shallow: many aliases of a cheap field add up.
func TestComplexityLimit(t *testing.T) {
	f := newFixture(t)
	_, cookie, csrf := f.login(t, "correct horse battery")
	post := func(aliases int) string {
		var q strings.Builder
		q.WriteString(`{"query":"{`)
		for i := range aliases {
			fmt.Fprintf(&q, " m%d: me { username }", i)
		}
		q.WriteString(` }"}`)
		return f.do(http.MethodPost, "/admin/graphql", q.String(), map[string]string{csrfHeader: csrf}, cookie).Body.String()
	}
	// me { username } costs 2.
	if body := post(maxQueryComplexity / 2); !strings.Contains(body, `"m0":{"username":"root"}`) {
		t.Fatalf("an operation at the limit must run: %s", body)
	}
	if body := post(maxQueryComplexity/2 + 1); !strings.Contains(body, "COMPLEXITY_LIMIT_EXCEEDED") {
		t.Fatalf("an operation above the limit must be refused: %.300s", body)
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

// Behind a gateway path prefix, the cookie and redirects carry it.
func TestPathPrefixInCookieAndRedirects(t *testing.T) {
	f := newFixture(t)
	f.h = New(Config{Store: f.h.cfg.Store, Accounts: f.accounts, Now: f.h.cfg.Now, PathPrefix: "/sql-mcp-server"})
	_, cookie, _ := f.login(t, "correct horse battery")
	if cookie == nil || cookie.Path != "/sql-mcp-server/admin" {
		t.Fatalf("session cookie = %+v", cookie)
	}
	rec := f.do(http.MethodGet, "/admin", "", nil, nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/sql-mcp-server/admin/" {
		t.Fatalf("redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	plain := newFixture(t)
	if rec := plain.do(http.MethodGet, "/admin", "", nil, nil); rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("without a prefix: %q", rec.Header().Get("Location"))
	}
}

// Under a gateway prefix, the playground's sign-in page posts relative to
// itself and its queries go to the prefixed GraphQL path.
func TestPlaygroundPathsUnderAGatewayPrefix(t *testing.T) {
	f := newFixture(t)
	f.h = New(Config{Store: f.h.cfg.Store, Accounts: f.accounts, Now: f.h.cfg.Now, Playground: true,
		PathPrefix: "/sql-mcp-server"})
	signIn := f.do(http.MethodGet, "/admin/playground", "", nil, nil).Body.String()
	if !strings.Contains(signIn, "fetch('login'") || strings.Contains(signIn, "/admin/login") {
		t.Fatalf("sign-in page calls an absolute path: %s", signIn)
	}
	_, cookie, _ := f.login(t, "correct horse battery")
	// The endpoint is appended to location.host: it must be the absolute
	// path under the prefix.
	rec := f.do(http.MethodGet, "/admin/playground", "", nil, cookie)
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") {
		t.Fatalf("the playground, holding the CSRF token, is served with policy %q", csp)
	}
	page := rec.Body.String()
	if !strings.Contains(page, `const url = location.protocol + '//' + location.host + "/sql-mcp-server/admin/graphql";`) {
		i := strings.Index(page, "location.host")
		t.Fatalf("playground endpoint: %s", page[i:min(len(page), i+80)])
	}
}

// The configuration MCP endpoint takes administrators' tokens, never the
// console's session cookie, and is served only with a store for tokens.
func TestConfigMCPEndpointNeedsAToken(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(http.MethodPost, "/admin/mcp", "{}", nil, nil); rec.Code == http.StatusUnauthorized {
		t.Fatal("without a token store the endpoint must not be served")
	}
	spec := configstore.Spec{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "store.db")}
	if err := configstore.Init(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	store, err := configstore.Open(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.h = New(Config{Store: f.h.cfg.Store, Accounts: f.accounts, Plans: store, Now: f.h.cfg.Now})
	_, cookie, csrf := f.login(t, "correct horse battery")
	rec := f.do(http.MethodPost, "/admin/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		map[string]string{csrfHeader: csrf}, cookie)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("a session cookie reached the endpoint: %d", rec.Code)
	}
}

// failingAccounts fails to read accounts while fail is set.
type failingAccounts struct {
	accounts.Store
	fail bool
}

func (a *failingAccounts) GetAdmin(ctx context.Context, username string) (accounts.Account, error) {
	if a.fail {
		return accounts.Account{}, context.Canceled
	}
	return a.Store.GetAdmin(ctx, username)
}

// An account that cannot be read now (the store failing, the request
// ending) answers 503 and keeps the session: only an account gone,
// disabled or with another password signs it out.
func TestSessionSurvivesAnAccountNotReadNow(t *testing.T) {
	f := newFixture(t)
	store := &failingAccounts{Store: f.accounts}
	f.h = New(Config{Store: f.h.cfg.Store, Accounts: store, Now: f.h.cfg.Now})
	_, cookie, csrf := f.login(t, "correct horse battery")
	ok := map[string]string{csrfHeader: csrf}
	store.fail = true
	if rec := f.do(http.MethodPost, "/admin/graphql", meQuery, ok, cookie); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("account not read: %d", rec.Code)
	}
	store.fail = false
	if rec := f.do(http.MethodPost, "/admin/graphql", meQuery, ok, cookie); rec.Code != http.StatusOK {
		t.Fatalf("the session was lost: %d %s", rec.Code, rec.Body)
	}
}
