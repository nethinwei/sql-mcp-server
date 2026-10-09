package mcpserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func applyHTTPDefaults(cfg *HTTPConfig) {
	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = 10 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 120 * time.Second
	}
	if cfg.MaxHeaderBytes <= 0 {
		cfg.MaxHeaderBytes = 1 << 20 // 1 MiB
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 4 << 20 // 4 MiB
	}
	if cfg.SessionTimeout <= 0 {
		cfg.SessionTimeout = 5 * time.Minute
	}
}

// sessionHandler is /mcp before authentication: MCP sessions bound to the
// identity that opened them, dropped when that identity is revoked.
func sessionHandler(s *mcp.Server, cfg HTTPConfig) http.Handler {
	identities := newSessionIdentityStore()
	eventStore := &sessionEventStore{
		EventStore: mcp.NewMemoryEventStore(nil), onClosed: cfg.OnSessionClosed, identity: identities,
	}
	handler := http.Handler(mcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{EventStore: eventStore, SessionTimeout: cfg.SessionTimeout},
	))
	handler = bindSessionIdentity(identities, handler)
	if cfg.RevokedPrincipals != nil {
		cfg.RevokedPrincipals(revokeSessions(identities, cfg.OnSessionClosed))
	}
	return handler
}

// authenticated wraps next with cfg's authentication: client certificates
// (verified by clientCAs under mTLS), proxy identity, body cap, bearer tokens
// and users.
func authenticated(cfg HTTPConfig, clientCAs *x509.CertPool, next http.Handler) http.Handler {
	handler := next
	if cfg.TrustProxyHeaders {
		handler = withProxyIdentity(cfg.Users, handler)
		if !cfg.mtlsEnabled() {
			networks, _ := parseTrustedProxyCIDRs(cfg.TrustedProxyCIDRs)
			handler = trustedProxyOnly(networks, handler)
		}
	}
	handler = limitBody(cfg.MaxBodyBytes, handler)
	handler = principalAuth(cfg.Token, cfg.Users, cfg.mtlsEnabled() || cfg.TrustProxyHeaders, handler)
	if cfg.mtlsEnabled() {
		handler = requireClientCert(clientCAs, handler)
	}
	return handler
}

// requireClientCert serves only requests whose connection presented a client
// certificate the current client CAs verify. The TLS handshake checks it too,
// but a connection outlives a change of authentication: one opened before
// mTLS was switched on, or under CAs since replaced, must not pass for it.
func requireClientCert(clientCAs *x509.CertPool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		opts := x509.VerifyOptions{
			Roots: clientCAs, Intermediates: x509.NewCertPool(),
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		for _, c := range r.TLS.PeerCertificates[1:] {
			opts.Intermediates.AddCert(c)
		}
		if _, err := r.TLS.PeerCertificates[0].Verify(opts); err != nil {
			http.Error(w, "client certificate not accepted", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func buildHTTPMux(mcpHandler, metrics http.Handler, cfg HTTPConfig) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	// /healthz is liveness only: the process is up and serving HTTP. Snapshot
	// and database readiness are separate so orchestrators can distinguish
	// "restart me" from "do not route traffic to me yet".
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/readyz/snapshot", staleMarker(cfg.SnapshotStale, readinessHandler(cfg.SnapshotReady)))
	mux.Handle("/readyz/db", readinessHandler(cfg.DatabaseReady))
	if cfg.Admin != nil {
		mux.Handle("/admin/", limitBody(cfg.MaxBodyBytes, cfg.Admin))
	}
	if metrics != nil {
		mux.Handle("/metrics", metrics)
	}
	return mux
}

// readinessProbeTimeout bounds one readiness check so a hung database cannot
// stall probe requests indefinitely.
const readinessProbeTimeout = 5 * time.Second

// readinessHandler serves one readiness probe, failing closed: a missing
// probe or a probe error returns 503. The body never echoes probe error
// details because these endpoints are unauthenticated like /healthz; the
// cause belongs in server logs, not on the wire.
func readinessHandler(probe func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if probe == nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), readinessProbeTimeout)
		defer cancel()
		if err := probe(ctx); err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// staleMarker adds X-Snapshot-Stale with the ID of a published store
// revision the server has not applied. The probe stays 200 so orchestrators
// keep routing to an instance that serves its last good snapshot; only the
// revision ID is exposed because the endpoint is unauthenticated.
func staleMarker(stale func() int64, next http.Handler) http.Handler {
	if stale == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := stale(); id > 0 {
			w.Header().Set("X-Snapshot-Stale", strconv.FormatInt(id, 10))
		}
		next.ServeHTTP(w, r)
	})
}

// loadTLS reads cfg's server certificate and, for mTLS, the client CAs.
func loadTLS(cfg HTTPConfig) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("mcpserver: load server certificate: %w", err)
	}
	out := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if !cfg.mtlsEnabled() {
		return out, nil
	}
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(cfg.ClientCA)
	if err != nil {
		return nil, fmt.Errorf("mcpserver: read client CA: %w", err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("mcpserver: client CA file contains no valid certificates")
	}
	out.ClientCAs, out.ClientAuth = pool, tls.RequireAndVerifyClientCert
	return out, nil
}

func serveHTTPWithShutdown(ctx context.Context, srv *http.Server, tlsEnabled bool) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	var err error
	if tlsEnabled {
		err = srv.ListenAndServeTLS("", "") // certificates from srv.TLSConfig
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// httpServing serves through the handlers and TLS settings of the current
// authentication, which apply replaces while serving.
type httpServing struct {
	cfg     HTTPConfig // as started; Addr and serving TLS or not are fixed
	session http.Handler
	mcp     atomic.Pointer[http.Handler]
	metrics atomic.Pointer[http.Handler]
	tls     atomic.Pointer[tls.Config]
}

func newHTTPServing(s *mcp.Server, cfg HTTPConfig) (*httpServing, error) {
	if err := validateHTTPSecurity(cfg); err != nil {
		return nil, err
	}
	applyHTTPDefaults(&cfg)
	h := &httpServing{cfg: cfg, session: sessionHandler(s, cfg)}
	prepared, err := PrepareHTTPAuth(cfg.Addr, cfg.auth())
	if err != nil {
		return nil, err
	}
	if err := h.apply(prepared); err != nil {
		return nil, err
	}
	if cfg.AuthChanges != nil {
		cfg.AuthChanges(h.apply)
	}
	return h, nil
}

// apply switches to a prepared authentication; it reads no file. Serving
// TLS or not stays as the listener started.
func (h *httpServing) apply(prepared PreparedAuth) error {
	cfg := h.cfg
	cfg.setAuth(prepared.auth)
	if cfg.tlsEnabled() != h.cfg.tlsEnabled() {
		return errors.New("mcpserver: serving TLS or not changes only with a restart")
	}
	var clientCAs *x509.CertPool
	if prepared.tls != nil {
		clientCAs = prepared.tls.ClientCAs
	}
	mcpHandler := authenticated(cfg, clientCAs, h.session)
	h.mcp.Store(&mcpHandler)
	if cfg.Metrics != nil {
		metrics := principalAuth(cfg.Token, cfg.Users, cfg.mtlsEnabled(), cfg.Metrics)
		if cfg.mtlsEnabled() {
			metrics = requireClientCert(clientCAs, metrics)
		}
		h.metrics.Store(&metrics)
	}
	h.tls.Store(prepared.tls)
	return nil
}

func (h *httpServing) handler() http.Handler {
	current := func(p *atomic.Pointer[http.Handler]) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*p.Load()).ServeHTTP(w, r) })
	}
	var metrics http.Handler
	if h.cfg.Metrics != nil {
		metrics = current(&h.metrics)
	}
	return buildHTTPMux(current(&h.mcp), metrics, h.cfg)
}

// Handler returns the fully hardened HTTP handler (token auth, body caps,
// proxy trust, session identity binding, /mcp, /healthz, optional /metrics)
// without binding a listener. ServeHTTP serves this same handler; tests and
// embedders can mount it on their own server so the middleware chain under
// test is identical to production.
func Handler(s *mcp.Server, cfg HTTPConfig) (http.Handler, error) {
	h, err := newHTTPServing(s, cfg)
	if err != nil {
		return nil, err
	}
	return h.handler(), nil
}

// ServeHTTP runs the server on streamable HTTP with authentication, request
// hardening (timeouts, header/body caps), a /healthz check, and an optional
// /metrics endpoint. See HTTPConfig for the security model.
func ServeHTTP(ctx context.Context, s *mcp.Server, cfg HTTPConfig) error {
	h, err := newHTTPServing(s, cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              h.cfg.Addr,
		Handler:           h.handler(),
		ReadHeaderTimeout: h.cfg.ReadHeaderTimeout,
		ReadTimeout:       h.cfg.ReadTimeout,
		WriteTimeout:      h.cfg.WriteTimeout,
		IdleTimeout:       h.cfg.IdleTimeout,
		MaxHeaderBytes:    h.cfg.MaxHeaderBytes,
	}
	if h.cfg.tlsEnabled() {
		srv.TLSConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return h.tls.Load(), nil },
		}
	}
	return serveHTTPWithShutdown(ctx, srv, h.cfg.tlsEnabled())
}
