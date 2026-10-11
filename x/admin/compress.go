package admin

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// gzipWriters reuses compressors across responses.
var gzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// compressed gzips responses for clients that accept it. It wraps the
// GraphQL replies only (a whole configuration is large): the console's pages
// and scripts stay as they are, since a gateway serving the console under a
// path prefix may rewrite the paths in them, which it cannot in a compressed
// body. The event stream is not wrapped either.
func compressed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzipWriters.Get().(*gzip.Writer)
		gz.Reset(w)
		defer gzipWriters.Put(gz)
		gw := &gzipResponse{ResponseWriter: w, gz: gz}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// acceptsGzip reports that the request's Accept-Encoding allows gzip with a
// nonzero quality, by name or through "*" when gzip is not listed (RFC 9110
// section 12.5.3): "gzip;q=0" refuses it.
func acceptsGzip(r *http.Request) bool {
	gzip, wildcard := -1.0, -1.0
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := quality(params)
		switch {
		case strings.EqualFold(strings.TrimSpace(name), "gzip"):
			gzip = q
		case strings.TrimSpace(name) == "*":
			wildcard = q
		}
	}
	if gzip >= 0 {
		return gzip > 0
	}
	return wildcard > 0
}

// quality is the q parameter of an Accept-Encoding entry, 1 when absent and 0
// when malformed.
func quality(params string) float64 {
	for _, p := range strings.Split(params, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || q < 0 {
			return 0
		}
		return q
	}
	return 1
}

// gzipResponse compresses the body once the handler writes one; a response
// without a body (304, 204) is passed through as it is.
type gzipResponse struct {
	http.ResponseWriter
	gz      *gzip.Writer
	started bool
	encoded bool
}

func (g *gzipResponse) WriteHeader(status int) {
	if !g.started {
		g.started = true
		h := g.Header()
		if status != http.StatusNoContent && status != http.StatusNotModified && h.Get("Content-Encoding") == "" {
			g.encoded = true
			h.Set("Content-Encoding", "gzip")
			h.Del("Content-Length")
		}
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponse) Write(b []byte) (int, error) {
	if !g.started {
		g.WriteHeader(http.StatusOK)
	}
	if !g.encoded {
		return g.ResponseWriter.Write(b)
	}
	return g.gz.Write(b)
}

func (g *gzipResponse) close() {
	if g.encoded {
		_ = g.gz.Close()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (g *gzipResponse) Unwrap() http.ResponseWriter { return g.ResponseWriter }
