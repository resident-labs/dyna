//go:build linux

package harnessconfig

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// restartOnOutdated returns the restart for a session's shell MCP server: it
// re-execs the server through the path that started it. An installed update
// has replaced the binary at that path, and the exec inherits the server's
// stdio, so the harness keeps its connection while the next request runs on
// the current build: a schema bump no longer cuts a running session's shells
// off (D-171). Exec returns only when it fails, in which case the server
// keeps answering with the error it already reported.
func restartOnOutdated(argv []string) func() {
	return func() {
		for _, target := range restartTargets(argv) {
			if err := syscall.Exec(target, argv, os.Environ()); err == nil {
				return
			}
		}
	}
}

// restartTargets names the executables a restart may exec, in order: the
// start path first, then this process's own path once an update has replaced
// its file. A path that still names the running image is skipped: that build
// is already the newest the path offers, and exec-ing it would only lose the
// failing call's answer again.
func restartTargets(argv []string) []string {
	var candidates []string
	if len(argv) > 0 && strings.ContainsRune(argv[0], os.PathSeparator) {
		candidates = append(candidates, argv[0])
	}
	if exe, err := os.Executable(); err == nil {
		// /proc/self/exe names a replaced file as "<path> (deleted)"; the
		// path itself now holds the new binary.
		candidates = append(candidates, strings.TrimSuffix(exe, " (deleted)"))
	}
	self, _ := os.Stat("/proc/self/exe")
	var targets []string
	seen := map[string]bool{}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0100 == 0 {
			continue
		}
		if self != nil && sameFile(info, self) {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil && seen[resolved] {
			continue
		} else if err == nil {
			seen[resolved] = true
		} else if seen[candidate] {
			continue
		} else {
			seen[candidate] = true
		}
		targets = append(targets, candidate)
	}
	return targets
}

// sameFile reports whether two stat results name one file.
func sameFile(a, b os.FileInfo) bool {
	as, aok := a.Sys().(*syscall.Stat_t)
	bs, bok := b.Sys().(*syscall.Stat_t)
	return aok && bok && as.Dev == bs.Dev && as.Ino == bs.Ino
}
