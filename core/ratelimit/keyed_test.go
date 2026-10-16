package ratelimit

import (
	"errors"
	"testing"
)

func TestKeyedBoundsEachKey(t *testing.T) {
	t.Parallel()
	k := NewKeyed()
	k.Update(map[string]KeyLimits{"user:a": {MaxInflight: 1}, "user:b": {RPS: 1}})
	release, err := k.Acquire("user:a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Acquire("user:a"); !errors.Is(err, ErrKeyInflight) {
		t.Fatalf("second call in flight: %v", err)
	}
	if _, err := k.Acquire("user:c"); err != nil {
		t.Fatalf("a key without limits: %v", err)
	}
	release()
	release() // releasing twice counts once
	if _, err := k.Acquire("user:a"); err != nil {
		t.Fatalf("after release: %v", err)
	}
	first, err := k.Acquire("user:b")
	if err != nil {
		t.Fatal(err)
	}
	first()
	if _, err := k.Acquire("user:b"); !errors.Is(err, ErrKeyRate) {
		t.Fatalf("past the rate: %v", err)
	}
	// A reload keeping the limits keeps the state; lifting them admits.
	k.Update(map[string]KeyLimits{"user:b": {RPS: 1}})
	if _, err := k.Acquire("user:b"); !errors.Is(err, ErrKeyRate) {
		t.Fatalf("a reload reset the rate: %v", err)
	}
	k.Update(nil)
	if _, err := k.Acquire("user:b"); err != nil {
		t.Fatalf("lifted limits: %v", err)
	}
}
