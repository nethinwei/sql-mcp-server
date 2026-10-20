package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	next.Server.Addr = ":9"
	next.Server.Auth.TLS.Cert, next.Server.Auth.TLS.Key = "cert.pem", "key.pem"
	if changed := RestartChanges(old, next); len(changed) != 2 {
		t.Fatalf("real changes = %v", changed)
	}
}

// A published revision that no longer loads (e.g. after a removed field) must
// not lock the store: an explicit restart-required publish replaces it.
func TestCheckPublishableRecoversFromUnloadablePublished(t *testing.T) {
	t.Parallel()
	valid := []byte("database:\n  driver: postgres\n  dsn: postgres://u@h/db\n" +
		"entities:\n  - name: users\n    fields: [{name: id}]\n")
	stale := append(append([]byte{}, valid...), "rateLimit:\n  removedField: 1\n"...)
	current := revision.Revision{ID: 1, Payload: stale, ContentHash: revision.Hash(stale)}
	target := revision.Revision{ID: 2, Payload: valid, ContentHash: revision.Hash(valid)}
	if err := CheckPublishable(current, target, false); err == nil {
		t.Fatal("publishing over an unloadable revision must require restart-required")
	}
	if err := CheckPublishable(current, target, true); err != nil {
		t.Fatalf("restart-required publish over an unloadable revision: %v", err)
	}
}

// CheckOffline refuses what assembling refuses without a database, even in a
// configuration built in code that never went through validation.
func TestCheckOfflineRefusesWhatAssemblyRefuses(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "postgres", DSN: "x"},
		Entities: []config.EntityConfig{{Name: "users", Fields: []config.FieldConfig{{Name: "id", Mask: "nope"}}}},
	}
	cfg.ApplyDefaults()
	if err := CheckOffline(cfg); err == nil || !strings.Contains(err.Error(), `unknown mask rule "nope"`) {
		t.Fatalf("CheckOffline = %v", err)
	}
}

// Whatever validates also passes the offline assembly checks: validation must
// not accept a configuration that publishes but then cannot load.
func TestValidConfigurationsPassCheckOffline(t *testing.T) {
	t.Parallel()
	var cases []struct {
		Name   string          `json:"name"`
		Config json.RawMessage `json:"config"`
		Error  string          `json:"error"`
	}
	data, err := os.ReadFile("../../core/config/testdata/rules.json")
	if err == nil {
		err = json.Unmarshal(data, &cases)
	}
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string][]byte{}
	for _, c := range cases {
		if c.Error == "" {
			inputs[c.Name] = c.Config
		}
	}
	paths, _ := filepath.Glob("../../examples/*/config.yaml")
	for _, p := range append(paths, "../../examples/config.example.yaml") {
		if inputs[p], err = os.ReadFile(p); err != nil {
			t.Fatal(err)
		}
	}
	for name, input := range inputs {
		cfg, err := LoadBytes(input)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := CheckOffline(cfg); err != nil {
			t.Errorf("%s validates but cannot be assembled: %v", name, err)
		}
	}
}

// laggingSummary reports the published revision as it was at summary while
// the store has published another since.
type laggingSummary struct {
	revision.Store
	summary revision.Revision
}

func (s laggingSummary) PublishedSummary(context.Context) (revision.Revision, error) {
	return s.summary, nil
}

// A revision published between the summary a poll reads and the revision it
// loads is the one that failed: it is the one reported stale.
func TestWatchStoreReportsTheRevisionLoaded(t *testing.T) {
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
	summary := publishPayload(t, store, "ok-2")
	broken := publishPayload(t, store, "broken")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = runtime.WatchStore(ctx, laggingSummary{store, summary}, first.ID, time.Millisecond, build, func(error) {})
	}()
	waitFor(t, func() bool { _, ok := runtime.Stale(); return ok })
	if s, _ := runtime.Stale(); s.RevisionID != broken.ID {
		t.Fatalf("stale revision = %d, want #%d (the one loaded), not #%d", s.RevisionID, broken.ID, summary.ID)
	}
}
