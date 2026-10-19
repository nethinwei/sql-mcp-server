package admin

import (
	"container/list"
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
	loginLockout           = time.Minute // doubling with each lockout in a row
	maxLoginLockout        = time.Hour
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
	mu   sync.Mutex
	byID map[string]*session
	// failures tracks at most maxTrackedLogins names, in byLast from the
	// least recent failure; names it cannot track share overflow, so a
	// flood of names neither grows it nor evicts a lockout still in force.
	failures map[string]*loginFailures
	byLast   *list.List
	overflow loginFailures
	now      func() time.Time
}

type loginFailures struct {
	count       int
	lockedUntil time.Time
	// lockouts counts the lockouts in a row, none ending in a sign-in nor
	// followed by maxLoginLockout without failures.
	lockouts int
	last     time.Time
	elem     *list.Element
}

// quiet reports whether f no longer affects sign-ins: maxLoginLockout has
// passed since its last failure and since its lockout ended.
func (f *loginFailures) quiet(now time.Time) bool {
	return now.After(f.last.Add(maxLoginLockout)) && now.After(f.lockedUntil.Add(maxLoginLockout))
}

func newSessions(now func() time.Time) *sessions {
	if now == nil {
		now = time.Now
	}
	return &sessions{
		byID: map[string]*session{}, failures: map[string]*loginFailures{}, byLast: list.New(), now: now,
	}
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

// alive reports that a session exists and has not expired, without counting
// as activity: a long-lived event stream must not keep a session from idling
// out.
func (s *sessions) alive(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	return ok && !s.expired(sess, s.now())
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
	return !s.now().Before(s.failuresOf(username, false).lockedUntil)
}

// recordLogin tracks failures; maxLoginFailures in a row lock the name, for
// loginLockout doubled with each lockout in a row up to maxLoginLockout, so
// a slow guesser waiting each lockout out is slowed down further.
func (s *sessions) recordLogin(username string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		if f, tracked := s.failures[username]; tracked {
			s.byLast.Remove(f.elem)
			delete(s.failures, username)
		}
		return
	}
	now := s.now()
	f := s.failuresOf(username, true)
	if now.After(f.lockedUntil.Add(maxLoginLockout)) {
		f.lockouts = 0
	}
	f.count++
	if f.count >= maxLoginFailures {
		f.count, f.lockedUntil = 0, now.Add(min(loginLockout<<min(f.lockouts, 6), maxLoginLockout))
		f.lockouts++
	}
	f.last = now
	if f.elem != nil {
		s.byLast.MoveToBack(f.elem)
	}
}

// failuresOf returns username's failures: its own when tracked, or, with
// track, when the table has room or a quiet name to evict for it; else the
// shared overflow. The zero failures of an untracked name allow a sign-in
// unless the table is full, when the overflow's lockout holds.
func (s *sessions) failuresOf(username string, track bool) *loginFailures {
	if f, tracked := s.failures[username]; tracked {
		return f
	}
	if len(s.failures) >= maxTrackedLogins {
		oldest := s.byLast.Front()
		if oldest == nil || !s.failures[oldest.Value.(string)].quiet(s.now()) {
			return &s.overflow
		}
		if track {
			delete(s.failures, s.byLast.Remove(oldest).(string))
		}
	}
	if !track {
		return &loginFailures{}
	}
	f := &loginFailures{elem: s.byLast.PushBack(username)}
	s.failures[username] = f
	return f
}
