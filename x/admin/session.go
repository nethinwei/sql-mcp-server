package admin

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// Session limits.
const (
	sessionIdleTimeout     = 8 * time.Hour
	sessionAbsoluteTimeout = 24 * time.Hour
	maxSessions            = 10000
	maxLoginFailures       = 5
	loginLockout           = time.Minute
	maxTrackedLogins       = 10000
)

var (
	errTooManySessions = errors.New("admin: too many active sessions")
	errLockedOut       = errors.New("admin: too many failed sign-ins; try again later")
	errBusy            = errors.New("admin: too many sign-ins in progress; try again later")
)

type session struct {
	username string
	// credential fingerprints the password hash at sign-in; a password
	// change ends every session started with the old one.
	credential string
	csrf       string
	created    time.Time
	lastSeen   time.Time
}

// sessions is the in-memory session table. Sessions do not survive a restart
// and are not shared between instances.
type sessions struct {
	mu       sync.Mutex
	byID     map[string]*session
	failures map[string]loginFailures
	now      func() time.Time
}

type loginFailures struct {
	count       int
	lockedUntil time.Time
}

func newSessions(now func() time.Time) *sessions {
	if now == nil {
		now = time.Now
	}
	return &sessions{byID: map[string]*session{}, failures: map[string]loginFailures{}, now: now}
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (s *sessions) expired(sess *session, now time.Time) bool {
	return now.Sub(sess.lastSeen) > sessionIdleTimeout || now.Sub(sess.created) > sessionAbsoluteTimeout
}

// create starts a session and returns its ID and CSRF token.
func (s *sessions) create(username, credential string) (id, csrf string, err error) {
	if id, err = randomToken(); err != nil {
		return "", "", err
	}
	if csrf, err = randomToken(); err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.byID) >= maxSessions {
		for k, sess := range s.byID {
			if s.expired(sess, now) {
				delete(s.byID, k)
			}
		}
		if len(s.byID) >= maxSessions {
			return "", "", errTooManySessions
		}
	}
	s.byID[id] = &session{username: username, credential: credential, csrf: csrf, created: now, lastSeen: now}
	return id, csrf, nil
}

// lookup returns a live session and refreshes its idle timer.
func (s *sessions) lookup(id string) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return session{}, false
	}
	now := s.now()
	if s.expired(sess, now) {
		delete(s.byID, id)
		return session{}, false
	}
	sess.lastSeen = now
	return *sess, true
}

func (s *sessions) delete(id string) {
	s.mu.Lock()
	delete(s.byID, id)
	s.mu.Unlock()
}

// allowLogin reports whether username may attempt a sign-in now.
func (s *sessions) allowLogin(username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.now().Before(s.failures[username].lockedUntil)
}

// recordLogin tracks failures; maxLoginFailures in a row lock the name.
func (s *sessions) recordLogin(username string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		delete(s.failures, username)
		return
	}
	if _, tracked := s.failures[username]; !tracked && len(s.failures) >= maxTrackedLogins {
		now := s.now()
		for k, f := range s.failures {
			if now.After(f.lockedUntil) {
				delete(s.failures, k)
			}
		}
	}
	f := s.failures[username]
	f.count++
	if f.count >= maxLoginFailures {
		f.count, f.lockedUntil = 0, s.now().Add(loginLockout)
	}
	s.failures[username] = f
}
