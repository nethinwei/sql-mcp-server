package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
)

func TestValidateStorePayloadRejectsPlaintextSecrets(t *testing.T) {
	t.Parallel()
	ok := []string{
		"${DSN}",
		"postgres://app:${PG_PASSWORD}@db:5432/app",
		"app:${file:/run/secrets/mysql}@tcp(db:3306)/app",
		"host=db user=app password=${PW} dbname=app",
		"postgres://app@db/app",
	}
	for _, dsn := range ok {
		cfg := &config.Config{Database: config.DatabaseConfig{Driver: "postgres", DSN: dsn}}
		if err := ValidateStorePayload(cfg); err != nil {
			t.Errorf("%q must be accepted: %v", dsn, err)
		}
	}
	bad := []string{
		"postgres://app:hunter2@db/app",
		"app:hunter2@tcp(db:3306)/app",
		"host=db password=hunter2",
	}
	for _, dsn := range bad {
		cfg := &config.Config{Databases: map[string]config.DatabaseConfig{"main": {Driver: "postgres", DSN: dsn}}}
		if err := ValidateStorePayload(cfg); !errors.Is(err, ErrPlaintextSecret) {
			t.Errorf("%q must be rejected: %v", dsn, err)
		}
	}
	shared := &config.Config{Database: config.DatabaseConfig{Driver: "postgres", DSN: "${DSN}"}}
	shared.Server.Auth.Token = "s3cret"
	if err := ValidateStorePayload(shared); !errors.Is(err, ErrPlaintextSecret) {
		t.Fatalf("shared token must be rejected: %v", err)
	}
}

func publishPayload(t *testing.T, s revision.Store, payload string) revision.Revision {
	t.Helper()
	ctx := context.Background()
	d, err := s.Create(ctx, revision.Draft{Payload: []byte(payload)})
	if err != nil {
		t.Fatal(err)
	}
	cur, err := s.Published(ctx)
	if err != nil && !errors.Is(err, revision.ErrNoPublished) {
		t.Fatal(err)
	}
	r, err := s.Publish(ctx, d.ID, cur.ID, revision.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatchStoreAppliesFailsStaticAndRecovers(t *testing.T) {
	t.Parallel()
	store := revision.NewMemoryStore(nil)
	first := publishPayload(t, store, "ok-1")
	runtime := NewRuntimeWithBuilder(&App{DefaultRole: "ok-1"}, nil)
	defer runtime.Close()
	build := func(rev revision.Revision) (*App, error) {
		if string(rev.Payload) == "broken" {
			return nil, errors.New("assemble failed")
		}
		return &App{DefaultRole: string(rev.Payload)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reported []error
	errs := make(chan error, 16)
	go func() {
		_ = runtime.WatchStore(ctx, store, first.ID, time.Millisecond, build, func(err error) { errs <- err })
	}()

	publishPayload(t, store, "ok-2")
	waitFor(t, func() bool { return runtime.Current().DefaultRole == "ok-2" })

	broken := publishPayload(t, store, "broken")
	waitFor(t, func() bool { s, ok := runtime.Stale(); return ok && s.RevisionID == broken.ID })
	time.Sleep(20 * time.Millisecond)
	if runtime.Current().DefaultRole != "ok-2" {
		t.Fatal("a failing revision must keep the previous snapshot")
	}
	for len(errs) > 0 {
		reported = append(reported, <-errs)
	}
	if len(reported) != 1 {
		t.Fatalf("a persistent failure must be reported once, got %d: %v", len(reported), reported)
	}

	publishPayload(t, store, "ok-3")
	waitFor(t, func() bool { return runtime.Current().DefaultRole == "ok-3" })
	if _, ok := runtime.Stale(); ok {
		t.Fatal("a successful reload must clear the stale state")
	}
}

// tamperedStore returns a published revision whose payload no longer matches
// its stored hash.
type tamperedStore struct{ revision.Store }

func (s tamperedStore) Published(ctx context.Context) (revision.Revision, error) {
	r, err := s.Store.Published(ctx)
	r.Payload = []byte("tampered")
	return r, err
}

func TestWatchStoreRejectsTamperedPayload(t *testing.T) {
	t.Parallel()
	store := revision.NewMemoryStore(nil)
	publishPayload(t, store, "ok-1")
	runtime := NewRuntimeWithBuilder(&App{DefaultRole: "ok-1"}, nil)
	defer runtime.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	built := false
	go func() {
		_ = runtime.WatchStore(ctx, tamperedStore{store}, 0, time.Millisecond,
			func(revision.Revision) (*App, error) { built = true; return &App{}, nil }, nil)
	}()
	waitFor(t, func() bool { _, ok := runtime.Stale(); return ok })
	if s, _ := runtime.Stale(); built || s.Err == "" || runtime.Current().DefaultRole != "ok-1" {
		t.Fatalf("tampered payload must not be built: built=%v stale=%+v", built, s)
	}
}

func TestLoadBytesRejectsUnregisteredDriver(t *testing.T) {
	t.Parallel()
	if _, err := LoadBytes([]byte("database:\n  driver: nosuchdb\n  dsn: x\n")); !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("LoadBytes err = %v", err)
	}
}
