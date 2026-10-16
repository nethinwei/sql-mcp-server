package ratelimit

import (
	"errors"
	"sync"
)

// ErrKeyRate and ErrKeyInflight refuse a call past its key's own limits.
var (
	ErrKeyRate     = errors.New("ratelimit: the caller's request rate is exceeded")
	ErrKeyInflight = errors.New("ratelimit: the caller has too many requests in flight")
)

// KeyLimits bound the calls of one key; zero is unlimited.
type KeyLimits struct {
	RPS         float64
	MaxInflight int
}

// Keyed bounds the calls of each key (a caller) by limits of its own: a rate
// and a number in flight, across the caller's sessions. Its state outlives
// configuration reloads: Update replaces the limits and keeps the state of
// the keys whose limits stay.
type Keyed struct {
	mu   sync.Mutex
	keys map[string]*keyState
}

type keyState struct {
	limits   KeyLimits
	bucket   *TokenBucket
	inflight int
}

// NewKeyed returns a Keyed without limits.
func NewKeyed() *Keyed { return &Keyed{keys: map[string]*keyState{}} }

// Update sets the limits of each key; keys not in limits are unlimited.
func (k *Keyed) Update(limits map[string]KeyLimits) {
	k.mu.Lock()
	defer k.mu.Unlock()
	next := make(map[string]*keyState, len(limits))
	for key, l := range limits {
		if l == (KeyLimits{}) {
			continue
		}
		st := k.keys[key]
		switch {
		case st == nil:
			st = &keyState{limits: l, bucket: NewTokenBucket(l.RPS)}
		case st.limits.RPS != l.RPS:
			st.bucket = NewTokenBucket(l.RPS)
		}
		st.limits = l
		next[key] = st
	}
	// Calls in flight under a key that loses its limits release into a state
	// no longer kept.
	k.keys = next
}

// Acquire admits a call of key, or refuses it with ErrKeyRate or
// ErrKeyInflight; release ends an admitted call.
func (k *Keyed) Acquire(key string) (release func(), err error) {
	if k == nil {
		return func() {}, nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	st := k.keys[key]
	if st == nil {
		return func() {}, nil
	}
	if st.limits.MaxInflight > 0 && st.inflight >= st.limits.MaxInflight {
		return nil, ErrKeyInflight
	}
	if st.bucket.Allow() != nil {
		return nil, ErrKeyRate
	}
	st.inflight++
	var once sync.Once
	return func() {
		once.Do(func() {
			k.mu.Lock()
			st.inflight--
			k.mu.Unlock()
		})
	}, nil
}
