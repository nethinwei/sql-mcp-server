package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
)

func TestValidateStorePayloadRejectsPlaintextSecrets(t *testing.T) {
	t.Parallel()
	ok := [][2]string{
		{"postgres", "${DSN}"},
		{"postgres", "postgres://app:${PG_PASSWORD}@db:5432/app"},
		{"postgres", "postgres://app:${file:/run/secrets/pg}@db:5432/app"},
		{"mysql", "app:${file:/run/secrets/mysql}@tcp(db:3306)/app"},
		{"postgres", "host=db user=app password=${PW} dbname=app"},
		{"postgres", "host=db user=app password = '${PW}' dbname=app"},
		{"postgres", "postgres://app@db/app"},
		{"postgres", "postgres://alice:@localhost/test"},
		{"postgres", "host=db user=app password='' dbname=app"},
		{"mysql", "app:@tcp(db:3306)/app"},
	}
	for _, c := range ok {
		cfg := &config.Config{Database: config.DatabaseConfig{Driver: c[0], DSN: c[1]}}
		if err := ValidateStorePayload(cfg); err != nil {
			t.Errorf("%q must be accepted: %v", c[1], err)
		}
	}
	bad := [][2]string{
		{"postgres", "postgres://app:hunter2@db/app"},
		{"postgres", "postgres://app@db/app?password=hunter2"},
		{"postgres", "postgres://app@db/app?%70assword=hunter2"},
		{"postgres", "postgres://app@db/app?sslmode=disable&PASS%57ORD=hunter2"},
		{"mysql", "app:hunter2@tcp(db:3306)/app"},
		{"oceanbase", "app:${PW}x@tcp(db:2881)/app"},
		{"postgres", "host=db password=hunter2"},
		{"postgres", "host=db password = hunter2"},
		{"postgres", "host=db password='hunter 2'"},
		// The driver grammar rejects these; every grammar is tried instead.
		{"postgres", "app:hunter2@tcp(db:3306)/app"},
		{"unknown", "host=db password = hunter2"},
	}
	for _, c := range bad {
		cfg := &config.Config{Databases: map[string]config.DatabaseConfig{"main": {Driver: c[0], DSN: c[1]}}}
		if err := ValidateStorePayload(cfg); !errors.Is(err, ErrPlaintextSecret) {
			t.Errorf("%q must be rejected: %v", c[1], err)
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

	second := publishPayload(t, store, "ok-2")
	waitFor(t, func() bool { return runtime.Current().DefaultRole == "ok-2" })
	// The snapshot switches before the applied revision is recorded.
	waitFor(t, func() bool { return runtime.AppliedRevision() == second.ID })

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
	if s, _ := runtime.Stale(); s.RestartRequired || runtime.AppliedRevision() != second.ID {
		t.Fatalf("stale = %+v, applied = %d", s, runtime.AppliedRevision())
	}

	publishPayload(t, store, "ok-3")
	waitFor(t, func() bool { return runtime.Current().DefaultRole == "ok-3" })
	if _, ok := runtime.Stale(); ok {
		t.Fatal("a successful reload must clear the stale state")
	}
}

func TestWatchStoreMarksRestartRequired(t *testing.T) {
	t.Parallel()
	store := revision.NewMemoryStore(nil)
	first := publishPayload(t, store, "ok-1")
	runtime := NewRuntimeWithBuilder(&App{DefaultRole: "ok-1"}, nil)
	defer runtime.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = runtime.WatchStore(ctx, store, first.ID, time.Millisecond, func(revision.Revision) (*App, error) {
			return nil, fmt.Errorf("%w: server.addr", ErrRestartRequired)
		}, nil)
	}()
	restart := publishPayload(t, store, "restart")
	waitFor(t, func() bool { s, ok := runtime.Stale(); return ok && s.RevisionID == restart.ID && s.RestartRequired })
	if runtime.AppliedRevision() != first.ID {
		t.Fatalf("applied = %d, want %d", runtime.AppliedRevision(), first.ID)
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

// Encoding a configuration writes out every section; that alone must not
// look like a restart-required change.
func TestRestartChangesIgnoresEncodingOnlyDifferences(t *testing.T) {
	t.Parallel()
	raw := []byte("database: {driver: postgres, dsn: x}\nentities:\n  - name: orders\n")
	old, err := configyaml.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := configyaml.Encode(old)
	if err != nil {
		t.Fatal(err)
	}
	next, err := configyaml.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if changed := RestartChanges(old, next); len(changed) != 0 {
		t.Fatalf("encoding-only differences reported as %v", changed)
	}
	next.Server.Auth.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
	next.Tools.DeleteRecord = !next.Tools.DeleteRecord
	if changed := RestartChanges(old, next); len(changed) != 2 {
		t.Fatalf("real changes = %v", changed)
	}
}
