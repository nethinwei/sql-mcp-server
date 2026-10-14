package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// healthcheck succeeds on a 2xx answer only, and fails when nothing answers.
func TestHealthcheck(t *testing.T) {
	t.Parallel()
	ready := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	var out bytes.Buffer
	if err := runCLI(t.Context(), []string{"healthcheck", "--url", srv.URL}, &out); err != nil ||
		out.String() != "healthy\n" {
		t.Fatalf("ready: %v %q", err, out.String())
	}
	ready = false
	if err := runCLI(t.Context(), []string{"healthcheck", "--url", srv.URL}, &out); err == nil {
		t.Fatal("a 503 must fail the check")
	}
	srv.Close()
	if err := runCLI(t.Context(), []string{"healthcheck", "--url", srv.URL}, &out); err == nil {
		t.Fatal("no answer must fail the check")
	}
}
