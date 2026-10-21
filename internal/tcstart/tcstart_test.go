package tcstart

import (
	"errors"
	"fmt"
	"testing"

	"github.com/containerd/errdefs"
)

func TestRetryable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"no host port", errdefs.ErrNotFound, true},
		{"no host port, wrapped", fmt.Errorf("start: %w", errdefs.ErrNotFound), true},
		{"daemon cannot bind", errors.New("Error response from daemon: ports are not available: " +
			"exposing port TCP 127.0.0.1:55824 -> 127.0.0.1:0: listen tcp 127.0.0.1:55824: " +
			"bind: address already in use"), true},
		{"image pull", errors.New("Error response from daemon: pull access denied"), false},
	}
	for _, c := range cases {
		if got := retryable(c.err); got != c.want {
			t.Errorf("%s: retryable(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}
