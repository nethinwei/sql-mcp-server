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
	// least recent failure (failuresOf).
	failures map[string]*loginFailures
	byLast   *list.List
	now      func() time.Time
}

type loginFailures struct {
	count       int
	lockedUntil time.Time
	// lockouts counts the lockouts in a row, none ending in a sign-in nor
	// followed by maxLoginLockout without failures.
	lockouts int
	last     time.Time
	// known: the name is an account's.
	known bool
	elem  *list.Element
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

// allowLogin reports whether username may attempt a sign-in now. A name the
// table does not track is not gated, so a flood of unknown names cannot lock
// an account out.
func (s *sessions) allowLogin(username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, tracked := s.failures[username]
	return !tracked || !s.now().Before(f.lockedUntil)
}

// recordLogin tracks failures; maxLoginFailures in a row lock the name, for
// loginLockout doubled with each lockout in a row up to maxLoginLockout, so
// a slow guesser waiting each lockout out is slowed down further. known
// reports that username names an account.
func (s *sessions) recordLogin(username string, ok, known bool) {
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
	f := s.failuresOf(username, known)
	if f == nil {
		return
	}
	if now.After(f.lockedUntil.Add(maxLoginLockout)) {
		f.lockouts = 0
	}
	f.count++
	if f.count >= maxLoginFailures {
		f.count, f.lockedUntil = 0, now.Add(min(loginLockout<<min(f.lockouts, 6), maxLoginLockout))
		f.lockouts++
	}
	f.last = now
	s.byLast.MoveToBack(f.elem)
}

// failuresOf returns username's failures, tracking it when the table has
// room or a quiet name to evict for it, or, for an account (known), the
// least recent name that is none: a flood of unknown names neither grows the
// table nor evicts a lockout still in force, and accounts stay tracked. Nil
// when the name cannot be tracked.
func (s *sessions) failuresOf(username string, known bool) *loginFailures {
	if f, tracked := s.failures[username]; tracked {
		f.known = f.known || known
		return f
	}
	if len(s.failures) >= maxTrackedLogins {
		victim := s.evictable(known)
		if victim == nil {
			return nil
		}
		delete(s.failures, s.byLast.Remove(victim).(string))
	}
	f := &loginFailures{known: known, elem: s.byLast.PushBack(username)}
	s.failures[username] = f
	return f
}

// evictable returns the entry a new name may replace in a full table: the
// least recent when quiet, else, for an account, the least recent name that
// is none.
func (s *sessions) evictable(known bool) *list.Element {
	e := s.byLast.Front()
	if e != nil && s.failures[e.Value.(string)].quiet(s.now()) {
		return e
	}
	for ; known && e != nil; e = e.Next() {
		if !s.failures[e.Value.(string)].known {
			return e
		}
	}
	return nil
}
