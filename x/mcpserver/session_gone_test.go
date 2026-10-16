package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A request on a session the server no longer holds (expired, lost to a
// restart, revoked) gets 404, so the client initializes a new session; it
// never reaches the transport, where a revoked session may still be open.
func TestGoneSessionsAreNotFound(t *testing.T) {
	store := newSessionIdentityStore()
	reached := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(sessionIDHeader) == "" {
			w.Header().Set(sessionIDHeader, "s1")
		} else {
			reached++
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := withRequestSubject(bindSessionIdentity(store, next))
	call := func(method, session string) int {
		req := httptest.NewRequest(method, "/mcp", nil)
		req.Header.Set("X-MCP-Role", "reader")
		if session != "" {
			req.Header.Set(sessionIDHeader, session)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	call(http.MethodPost, "") // initialize: s1 bound to reader
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		if code := call(method, "expired"); code != http.StatusNotFound {
			t.Fatalf("%s on an unknown session = %d, want 404", method, code)
		}
	}
	if code := call(http.MethodPost, "s1"); code != http.StatusNoContent || reached != 1 {
		t.Fatalf("the bound session = %d (reached %d)", code, reached)
	}
	revokeSessions(store, nil, nil)([]string{"reader"})
	if code := call(http.MethodPost, "s1"); code != http.StatusNotFound || reached != 1 {
		t.Fatalf("a revoked session = %d (reached %d), want 404 and not handed on", code, reached)
	}
}
