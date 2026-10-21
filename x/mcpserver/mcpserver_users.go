package mcpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"net/http"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

// UserDirectory resolves configured users against the current snapshot.
// bootstrap.Runtime and bootstrap.App implement it.
type UserDirectory interface {
	UserByTokenHash(hash string) (bootstrap.UserIdentity, bool)
	UserByName(name string) (bootstrap.UserIdentity, bool)
	UserByCertificate(leaf *x509.Certificate) (bootstrap.UserIdentity, bool)
}

const userHeader = "X-MCP-User"

// forgedPrincipal stands in for a role that names a principal without being
// an authenticated user (a custom transport's WithSubject("user:alice")). No
// configured role or user is named so, so it holds no grant.
const forgedPrincipal = ":forged"

// namesPrincipal reports whether role is a principal key ("user:alice")
// rather than a role: role and user names cannot contain ':'.
func namesPrincipal(role string) bool { return strings.Contains(role, ":") }

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
		if !s.user && namesPrincipal(s.role) {
			return forgedPrincipal, nil
		}
		return s.role, s.attrs
	}
	if app.DefaultUser != "" {
		id, found := app.UserByName(app.DefaultUser)
		if !found {
			return bootstrap.UserPrincipal(app.DefaultUser), nil
		}
		return id.Principal, mergeSubject(s.attrs, id.Subject)
	}
	role, attrs := subjectFromContext(ctx, app.DefaultRole)
	if len(app.Users) > 0 && namesPrincipal(role) { // a --role naming a user's principal, as a forged header
		return forgedPrincipal, nil
	}
	return role, attrs
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
		if !ok && (token == "" && channelAuthenticated || certUser(r)) {
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
		id, ok := users.UserByName(rbac.NormalizeRole(name))
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
// the principals and returns them; the dropped binding makes the session's
// next request fail at once. Each is then reported closed, so its
// transactions roll back, and closed in the transport (closeOpen), in the
// background: closing waits for the session's requests running (a long
// query), and the caller, a reload, holds what every reload waits for.
func revokeSessions(
	b *bus.Bus, store *sessionIdentityStore, onClosed func(string), closeOpen func([]string),
) func([]string) []string {
	return func(principals []string) []string {
		sessions := store.sessionsFor(principals)
		for _, session := range sessions {
			store.close(session)
		}
		if len(sessions) > 0 && (onClosed != nil || closeOpen != nil) {
			b.Go("close revoked sessions", func(context.Context) {
				if onClosed != nil {
					for _, session := range sessions {
						onClosed(session)
					}
				}
				if closeOpen != nil {
					closeOpen(sessions)
				}
			})
		}
		return sessions
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

// certIdentity authenticates, under mTLS with users configured, a request
// without a bearer token by its client certificate (verified by
// requireClientCert): one matching a user's certSubjects calls as that user.
// One matching none is refused when requireUser is set (clientIdentity
// "user"), else it keeps the default identity. A trusted proxy's
// certificate, or address, is exempt: it names its callers by headers.
func certIdentity(
	users UserDirectory, requireUser bool, proxy func(*http.Request) bool, next http.Handler,
) http.Handler {
	if users == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, bearer := bearerToken(r); bearer || proxy(r) {
			next.ServeHTTP(w, r)
			return
		}
		if id, ok := users.UserByCertificate(r.TLS.PeerCertificates[0]); ok {
			next.ServeHTTP(w, r.WithContext(withUser(r.Context(), id, nil)))
			return
		}
		if requireUser {
			http.Error(w, "the client certificate names no user", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// certUser reports a request certIdentity authenticated as a user.
func certUser(r *http.Request) bool {
	s, ok := r.Context().Value(subjectCtxKey{}).(requestSubject)
	return ok && s.user
}
