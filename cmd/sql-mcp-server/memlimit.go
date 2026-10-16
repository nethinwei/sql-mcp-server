package main

import (
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// cgroupMemoryFiles hold a container's memory limit under cgroup v2 and v1.
var cgroupMemoryFiles = []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"}

// applyContainerMemoryLimit sets the Go runtime's soft memory limit to 90% of
// the container's when GOMEMLIMIT is unset. Without it the runtime does not
// know the limit: the garbage of assembling a large revision can reach it,
// and the process is killed instead of collecting.
func applyContainerMemoryLimit() {
	soft, ok := containerSoftLimit(os.Getenv, os.ReadFile)
	if !ok {
		return
	}
	debug.SetMemoryLimit(soft)
	slog.Info("memory limit set from the container (GOMEMLIMIT unset)", "bytes", soft)
}

// containerSoftLimit is 90% of the container's memory limit, unless
// GOMEMLIMIT is set or the container has no limit.
func containerSoftLimit(getenv func(string) string, readFile func(string) ([]byte, error)) (int64, bool) {
	if getenv("GOMEMLIMIT") != "" {
		return 0, false
	}
	for _, path := range cgroupMemoryFiles {
		raw, err := readFile(path)
		if err != nil {
			continue
		}
		limit, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		// "max" (v2) and a huge page-rounded value (v1) mean no limit.
		if err != nil || limit <= 0 || limit >= 1<<60 {
			return 0, false
		}
		return limit / 10 * 9, true
	}
	return 0, false
}
