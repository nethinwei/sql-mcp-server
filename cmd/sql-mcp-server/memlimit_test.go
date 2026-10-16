package main

import (
	"errors"
	"testing"
)

func TestContainerSoftLimit(t *testing.T) {
	t.Parallel()
	files := func(m map[string]string) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if v, ok := m[path]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("no such file")
		}
	}
	env := func(v string) func(string) string { return func(string) string { return v } }
	cases := map[string]struct {
		env   string
		files map[string]string
		want  int64
		ok    bool
	}{
		"cgroup v2": {files: map[string]string{"/sys/fs/cgroup/memory.max": "1073741820\n"}, want: 966367638, ok: true},
		"cgroup v1": {
			files: map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "1000"}, want: 900, ok: true,
		},
		"v2 unlimited":    {files: map[string]string{"/sys/fs/cgroup/memory.max": "max\n"}},
		"v1 unlimited":    {files: map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712"}},
		"no cgroup":       {},
		"GOMEMLIMIT wins": {env: "512MiB", files: map[string]string{"/sys/fs/cgroup/memory.max": "1000"}},
	}
	for name, c := range cases {
		got, ok := containerSoftLimit(env(c.env), files(c.files))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %d, %v; want %d, %v", name, got, ok, c.want, c.ok)
		}
	}
}
