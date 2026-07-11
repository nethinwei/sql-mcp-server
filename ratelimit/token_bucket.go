package ratelimit

import (
	"math"
	"sync"
	"time"
)

// TokenBucket is a bounded in-process request-rate limiter.
type TokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

// NewTokenBucket returns nil when rate is non-positive.
func NewTokenBucket(rate float64) *TokenBucket {
	if rate <= 0 {
		return nil
	}
	burst := math.Max(1, math.Ceil(rate))
	return &TokenBucket{rate: rate, burst: burst, tokens: burst, last: time.Now()}
}

// Allow consumes one token or returns ErrRateLimited.
func (b *TokenBucket) Allow() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = math.Min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return ErrRateLimited
	}
	b.tokens--
	return nil
}
