// Package childenv prepares child process environments.
package childenv

import (
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// WithoutLocks copies env without lock descriptor markers. A nil env inherits
// the current environment, like exec.Cmd.Env. An empty env remains empty.
// It also marks inherited lock descriptors close-on-exec without closing them.
// The descriptor numbers only describe intentional ExtraFiles transfers; they
// must not reach unrelated children, which may reuse those descriptor numbers.
func WithoutLocks(env []string) []string {
	// Inherited ExtraFiles are not CLOEXEC. Protect even a filtered command
	// environment, which may already omit the original process markers.
	if fd, err := strconv.Atoi(os.Getenv("DYNA_SUBAGENT_LOCK_FD")); err == nil && fd >= 3 {
		syscall.CloseOnExec(fd)
	}
	if env == nil {
		env = os.Environ()
	}
	clean := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key != "DYNA_SUBAGENT_LOCK_FD" {
			clean = append(clean, entry)
		}
	}
	return clean
}

// Clean removes lock markers after command environment overrides are complete.
// It preserves Cmd.Environ's working-directory handling and leaves ExtraFiles
// untouched. Intentional lock transfers must add their markers after cleaning.
// Inherited lock descriptors are marked CLOEXEC, but remain open in this process.
func Clean(cmd *exec.Cmd) *exec.Cmd {
	cmd.Env = WithoutLocks(cmd.Environ())
	return cmd
}

// Merge returns base with overrides applied, without DYNA_* entries, with term
// as TERM, sorted by key. A later entry replaces an earlier one. It reads no
// process state, so a caller passes the host environment as base.
func Merge(base, overrides []string, term string) []string {
	merged := map[string]string{}
	for _, list := range [][]string{base, overrides} {
		for _, item := range list {
			key, value, ok := strings.Cut(item, "=")
			if ok && !strings.HasPrefix(key, "DYNA_") {
				merged[key] = value
			}
		}
	}
	merged["TERM"] = term
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+merged[key])
	}
	return out
}
