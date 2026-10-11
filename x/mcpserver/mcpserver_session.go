package mcpserver

import (
	"net/http"
	"strings"
	"sync"
)

// The MCP session's identity: a session is bound to the identity that
// created it, and only that identity may use it (see bindSessionIdentity).

type sessionIdentityStore struct {
	mu       sync.RWMutex
	sessions map[string]sessionIdentity
}

func newSessionIdentityStore() *sessionIdentityStore {
	return &sessionIdentityStore{sessions: make(map[string]sessionIdentity)}
}

func (s *sessionIdentityStore) bind(sessionID string, identity sessionIdentity) bool {
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sessions[sessionID]; ok {
		return existing == identity
	}
	s.sessions[sessionID] = identity
	return true
}

func (s *sessionIdentityStore) matches(sessionID string, identity sessionIdentity) bool {
	s.mu.RLock()
	existing, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	return ok && existing == identity
}

func (s *sessionIdentityStore) close(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
}

// bindSessionIdentity binds a new MCP session to the identity that created
// it and refuses requests on it from any other. The binding is made as the
// initialize response's headers go out, not when its handler returns: the
// client sends its next request as soon as it has the session ID, and the
// handler may still be streaming the response then.
func bindSessionIdentity(store *sessionIdentityStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := requestIdentity(r.Context())
		sessionID := strings.TrimSpace(r.Header.Get(sessionIDHeader))
		switch r.Method {
		case http.MethodPost, http.MethodGet, http.MethodDelete:
			if sessionID != "" && !store.matches(sessionID, identity) {
				http.Error(w, "MCP session identity mismatch", http.StatusForbidden)
				return
			}
		}
		if sessionID != "" || r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		binder := &sessionBinder{ResponseWriter: w, store: store, identity: identity}
		next.ServeHTTP(binder, r)
		binder.bind()
	})
}

// sessionBinder binds the session ID a response announces to the identity
// of the request, before the first byte of the response is written.
type sessionBinder struct {
	http.ResponseWriter
	store    *sessionIdentityStore
	identity sessionIdentity
	bound    bool
}

func (b *sessionBinder) bind() {
	if b.bound {
		return
	}
	b.bound = true
	_ = b.store.bind(strings.TrimSpace(b.Header().Get(sessionIDHeader)), b.identity)
}

func (b *sessionBinder) WriteHeader(code int) {
	b.bind()
	b.ResponseWriter.WriteHeader(code)
}

func (b *sessionBinder) Write(p []byte) (int, error) {
	b.bind()
	return b.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the underlying writer (Flush,
// which the streaming transport needs).
func (b *sessionBinder) Unwrap() http.ResponseWriter { return b.ResponseWriter }

func (b *sessionBinder) Flush() {
	b.bind()
	if f, ok := b.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
