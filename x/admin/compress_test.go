package admin

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGraphQLResponsesAreCompressedForClientsThatAcceptIt(t *testing.T) {
	f := newFixture(t)
	_, cookie, csrf := f.login(t, "correct horse battery")
	headers := map[string]string{csrfHeader: csrf, "Accept-Encoding": "gzip, deflate, br"}
	rec := f.do(http.MethodPost, "/admin/graphql", meQuery, headers, cookie)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q", rec.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if !strings.Contains(string(body), `"username":"root"`) {
		t.Fatalf("body = %s", body)
	}
	delete(headers, "Accept-Encoding")
	plain := f.do(http.MethodPost, "/admin/graphql", meQuery, headers, cookie)
	if plain.Header().Get("Content-Encoding") != "" || !strings.Contains(plain.Body.String(), `"username":"root"`) {
		t.Fatalf("without Accept-Encoding: %q %s", plain.Header().Get("Content-Encoding"), plain.Body)
	}
}

// Pages and scripts are left uncompressed: a gateway may rewrite paths in them.
func TestConsolePagesAreNotCompressed(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodGet, "/admin/", "", map[string]string{"Accept-Encoding": "gzip"}, nil)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("console page Content-Encoding = %q", rec.Header().Get("Content-Encoding"))
	}
}

func TestAcceptsGzipHonorsQuality(t *testing.T) {
	t.Parallel()
	for header, want := range map[string]bool{
		"gzip": true, "gzip, deflate, br": true, "GZIP;q=0.5": true, "br;q=1, gzip ; q=0.001": true,
		"gzip;q=0": false, "gzip;q=0, identity": false, "gzip;q=0.0": false, "gzip;q=x": false,
		"*": true, "*;q=0": false, "gzip;q=0, *": false, "identity": false, "": false,
	} {
		r, _ := http.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", header)
		if got := acceptsGzip(r); got != want {
			t.Errorf("Accept-Encoding %q: gzip = %v, want %v", header, got, want)
		}
	}
}
