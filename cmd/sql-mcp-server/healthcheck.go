package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"
)

// runHealthcheck requests --url, by default the snapshot readiness of a
// server on the default address, and fails unless it answers 2xx: a
// container health check, the image having no shell or curl.
func runHealthcheck(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:8080/readyz/snapshot", "URL to request")
	timeout := fs.Duration("timeout", 3*time.Second, "how long to wait for the answer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("healthcheck accepts no arguments, got %q", fs.Args())
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("healthcheck: %s answered %s", *url, res.Status)
	}
	_, err = fmt.Fprintln(stdout, "healthy")
	return err
}
