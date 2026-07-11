package mcpserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
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

func buildMCPHandler(s *mcp.Server, cfg HTTPConfig) http.Handler {
	identities := newSessionIdentityStore()
	eventStore := &sessionEventStore{
		EventStore: mcp.NewMemoryEventStore(nil), onClosed: cfg.OnSessionClosed, identity: identities,
	}
	handler := http.Handler(mcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{EventStore: eventStore, SessionTimeout: cfg.SessionTimeout},
	))
	handler = bindSessionIdentity(identities, handler)
	if cfg.TrustProxyHeaders {
		handler = withRequestSubject(handler)
		if !cfg.mtlsEnabled() {
			networks, _ := parseTrustedProxyCIDRs(cfg.TrustedProxyCIDRs)
			handler = trustedProxyOnly(networks, handler)
		}
	}
	handler = limitBody(cfg.MaxBodyBytes, handler)
	if cfg.Token != "" {
		handler = tokenAuth(cfg.Token, handler)
	}
	return handler
}

func buildHTTPMux(mcpHandler http.Handler, cfg HTTPConfig) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	// /healthz is liveness only: the process is up and serving HTTP. Snapshot
	// and database readiness are separate so orchestrators can distinguish
	// "restart me" from "do not route traffic to me yet".
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/readyz/snapshot", readinessHandler(cfg.SnapshotReady))
	mux.Handle("/readyz/db", readinessHandler(cfg.DatabaseReady))
	if cfg.Metrics != nil {
		metricsHandler := cfg.Metrics
		if cfg.Token != "" {
			metricsHandler = tokenAuth(cfg.Token, metricsHandler)
		}
		mux.Handle("/metrics", metricsHandler)
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

func configureMCPTLS(cfg HTTPConfig, srv *http.Server) error {
	if !cfg.mtlsEnabled() {
		return nil
	}
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(cfg.ClientCA)
	if err != nil {
		return fmt.Errorf("mcpserver: read client CA: %w", err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		return errors.New("mcpserver: client CA file contains no valid certificates")
	}
	srv.TLSConfig = &tls.Config{
		ClientCAs:  pool,
		ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS12,
	}
	return nil
}

func serveHTTPWithShutdown(ctx context.Context, srv *http.Server, tlsEnabled bool, cert, key string) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	var err error
	if tlsEnabled {
		err = srv.ListenAndServeTLS(cert, key)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func validatedHandler(s *mcp.Server, cfg *HTTPConfig) (http.Handler, error) {
	if err := validateHTTPSecurity(*cfg); err != nil {
		return nil, err
	}
	applyHTTPDefaults(cfg)
	return buildHTTPMux(buildMCPHandler(s, *cfg), *cfg), nil
}

// Handler returns the fully hardened HTTP handler (token auth, body caps,
// proxy trust, session identity binding, /mcp, /healthz, optional /metrics)
// without binding a listener. ServeHTTP serves this same handler; tests and
// embedders can mount it on their own server so the middleware chain under
// test is identical to production.
func Handler(s *mcp.Server, cfg HTTPConfig) (http.Handler, error) {
	return validatedHandler(s, &cfg)
}

// ServeHTTP runs the server on streamable HTTP with authentication, request
// hardening (timeouts, header/body caps), a /healthz check, and an optional
// /metrics endpoint. See HTTPConfig for the security model.
func ServeHTTP(ctx context.Context, s *mcp.Server, cfg HTTPConfig) error {
	mux, err := validatedHandler(s, &cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
	if err := configureMCPTLS(cfg, srv); err != nil {
		return err
	}
	return serveHTTPWithShutdown(ctx, srv, cfg.tlsEnabled(), cfg.TLSCert, cfg.TLSKey)
}
