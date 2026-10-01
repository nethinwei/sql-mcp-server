package mcpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// UserDirectory resolves configured users against the current snapshot.
// bootstrap.Runtime and bootstrap.App implement it.
type UserDirectory interface {
	UserByTokenHash(hash string) (bootstrap.UserIdentity, bool)
	UserByName(name string) (bootstrap.UserIdentity, bool)
}

const userHeader = "X-MCP-User"

// withUser attaches an authenticated user's principal and configured subject.
func withUser(ctx context.Context, id bootstrap.UserIdentity, headerAttrs map[string]any) context.Context {
	return context.WithValue(ctx, subjectCtxKey{}, requestSubject{
		role: id.Principal, attrs: mergeSubject(headerAttrs, id.Subject), user: true,
	})
}

// mergeSubject overlays configured user attributes on header attributes, so a
// trusted proxy can add attributes but never rewrite a configured tenant.
func mergeSubject(header, configured map[string]any) map[string]any {
	if len(header) == 0 {
		return configured
	}
	out := make(map[string]any, len(header)+len(configured))
	for k, v := range header {
		out[k] = v
	}
	for k, v := range configured {
		out[k] = v
	}
	return out
}

// callerIdentity returns the request principal and subject. Requests without
// an identity use server.user when configured (fail closed to a grant-less
// principal if that user was removed), otherwise server.role.
func callerIdentity(ctx context.Context, app *bootstrap.App) (string, map[string]any) {
	s, ok := ctx.Value(subjectCtxKey{}).(requestSubject)
	if ok && s.role != "" {
		return s.role, s.attrs
	}
	if app.DefaultUser != "" {
		id, found := app.UserByName(app.DefaultUser)
		if !found {
			return bootstrap.UserPrincipal(app.DefaultUser), nil
		}
		return id.Principal, mergeSubject(s.attrs, id.Subject)
	}
	return subjectFromContext(ctx, app.DefaultRole)
}

// principalAuth authenticates bearer tokens. A token matching an enabled user
// yields that user's identity; the shared token keeps the default identity.
// Without a shared token, a request may omit the bearer only when the channel
// is already authenticated (mTLS or trusted proxy); otherwise configured users
// make a user token mandatory.
func principalAuth(token string, users UserDirectory, channelAuthenticated bool, next http.Handler) http.Handler {
	if users == nil {
		if token == "" {
			return next
		}
		return tokenAuth(token, next)
	}
	shared := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := bearerToken(r)
		if !ok && token == "" && channelAuthenticated {
			next.ServeHTTP(w, r)
			return
		}
		if ok {
			if id, found := users.UserByTokenHash(config.TokenHash(presented)); found {
				next.ServeHTTP(w, r.WithContext(withUser(r.Context(), id, nil)))
				return
			}
		}
		if token != "" {
			got := sha256.Sum256([]byte(presented))
			if subtle.ConstantTimeCompare(got[:], shared[:]) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func bearerToken(r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, presented, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	presented = strings.TrimSpace(presented)
	return presented, presented != ""
}

// withProxyIdentity reads trusted proxy identity headers. X-MCP-User must name
// an enabled user and cannot be combined with X-MCP-Role; a request already
// authenticated as a user must not carry proxy identity headers.
func withProxyIdentity(users UserDirectory, next http.Handler) http.Handler {
	legacy := withRequestSubject(next)
	if users == nil {
		return legacy
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(r.Header.Get(userHeader))
		role := r.Header.Get("X-MCP-Role")
		if s, ok := r.Context().Value(subjectCtxKey{}).(requestSubject); ok && s.user {
			if name != "" || role != "" || r.Header.Get("X-MCP-Subject") != "" {
				http.Error(w, "proxy identity headers conflict with an authenticated user", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if name == "" {
			legacy.ServeHTTP(w, r)
			return
		}
		if role != "" {
			http.Error(w, "X-MCP-User and X-MCP-Role are mutually exclusive", http.StatusBadRequest)
			return
		}
		id, ok := users.UserByName(canonicalRole(name))
		if !ok {
			http.Error(w, "unknown or disabled user", http.StatusForbidden)
			return
		}
		attrs, err := parseSubjectHeader(r.Header.Get("X-MCP-Subject"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), id, attrs)))
	})
}

// revokeSessions drops the identity binding of every session bound to one of
// the principals and reports each session as closed so its transactions roll
// back. The dropped binding makes the session's next request fail.
func revokeSessions(store *sessionIdentityStore, onClosed func(string)) func([]string) {
	return func(principals []string) {
		for _, session := range store.sessionsFor(principals) {
			store.close(session)
			if onClosed != nil {
				onClosed(session)
			}
		}
	}
}

func (s *sessionIdentityStore) sessionsFor(principals []string) []string {
	want := make(map[string]bool, len(principals))
	for _, p := range principals {
		want[p] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for session, identity := range s.sessions {
		if want[identity.role] {
			out = append(out, session)
		}
	}
	return out
}
