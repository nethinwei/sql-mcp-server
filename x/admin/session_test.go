package admin

import (
	"fmt"
	"testing"
	"time"
)

func lockOutName(s *sessions, name string) {
	for range maxLoginFailures {
		s.recordLogin(name, false, false)
	}
}

// The failure table stays bounded even when every tracked name is locked out.
func TestLoginFailuresBounded(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	s := newSessions(func() time.Time { return now })
	for i := range maxTrackedLogins + 100 {
		lockOutName(s, fmt.Sprintf("name%d", i))
	}
	if n := len(s.failures); n > maxTrackedLogins {
		t.Fatalf("tracked %d names, want at most %d", n, maxTrackedLogins)
	}
	// A name the table cannot track is not gated: it is no account's.
	lockOutName(s, "late")
	if !s.allowLogin("late") {
		t.Fatal("an untracked name was locked out")
	}
}

// A flood of unknown names locks no account out, and an account's failures
// are still tracked and lock it.
func TestNameFloodLocksNoAccountOut(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	s := newSessions(func() time.Time { return now })
	for i := range maxTrackedLogins + 100 {
		lockOutName(s, fmt.Sprintf("flood%d", i))
	}
	if !s.allowLogin("root") {
		t.Fatal("a flood of unknown names locked out an account that never failed")
	}
	for range maxLoginFailures {
		s.recordLogin("root", false, true)
	}
	if s.allowLogin("root") || len(s.failures) > maxTrackedLogins {
		t.Fatalf("an account's failures must be tracked in a full table (%d tracked)", len(s.failures))
	}
}

// A flood of names does not evict a name's lockout history: its next
// lockout still doubles.
func TestLockoutSurvivesNameFlood(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	s := newSessions(func() time.Time { return now })
	lockOutName(s, "root")
	now = now.Add(loginLockout + time.Second) // the first lockout is over
	for i := range maxTrackedLogins + 100 {
		s.recordLogin(fmt.Sprintf("flood%d", i), false, false)
	}
	lockOutName(s, "root")
	now = now.Add(2*loginLockout - time.Second)
	if s.allowLogin("root") {
		t.Fatal("the flood reset root's lockouts in a row")
	}
}

// Names quiet for maxLoginLockout make room for new ones.
func TestLoginFailuresEvictQuietNames(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	s := newSessions(func() time.Time { return now })
	for i := range maxTrackedLogins {
		s.recordLogin(fmt.Sprintf("old%d", i), false, false)
	}
	now = now.Add(maxLoginLockout + time.Second)
	lockOutName(s, "new")
	if _, tracked := s.failures["new"]; !tracked || len(s.failures) > maxTrackedLogins {
		t.Fatalf("new name tracked = %v, table %d", tracked, len(s.failures))
	}
}
