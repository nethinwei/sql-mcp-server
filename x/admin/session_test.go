package admin

import (
	"fmt"
	"testing"
	"time"
)

func lockOutName(s *sessions, name string) {
	for range maxLoginFailures {
		s.recordLogin(name, false)
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
	// Names the table cannot track share one lockout: guessing goes on
	// being limited.
	lockOutName(s, "late")
	if s.allowLogin("late") {
		t.Fatal("an untracked name escaped the lockout")
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
		s.recordLogin(fmt.Sprintf("flood%d", i), false)
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
		s.recordLogin(fmt.Sprintf("old%d", i), false)
	}
	now = now.Add(maxLoginLockout + time.Second)
	lockOutName(s, "new")
	if _, tracked := s.failures["new"]; !tracked || len(s.failures) > maxTrackedLogins {
		t.Fatalf("new name tracked = %v, table %d", tracked, len(s.failures))
	}
}
