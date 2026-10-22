// Package agentslice runs agent work in its own systemd scope below a shared
// dyna.slice. The kernel then shares the machine for us: the desktop wins CPU
// under contention, agents split CPU fairly among themselves, and an
// out-of-memory kill hits work inside Dyna instead of the desktop. When the
// systemd user manager is not available, commands start unchanged.
//
// Set DYNA_SLICE=off to disable it.
package agentslice

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/fsutil"
)

// Slice is the systemd slice that holds every agent scope.
const Slice = "dyna.slice"

// ForegroundSlice and ForegroundWeight keep Dyna UI services above agent work.
const ForegroundSlice = "app.slice"
const ForegroundWeight = "200"

// EngineUnit is the engine's own systemd user service.
const EngineUnit = "dyna-engine.service"

// EngineWeight keeps the engine level with the owner's other apps (100) and
// ten times the agent slice. The engine runs constantly; at the foreground
// weight its background work would outrank the owner's apps.
const EngineWeight = "100"

// managedHeader marks a slice file Dyna may rewrite. Remove the line to keep
// local edits.
const managedHeader = "# Managed by Dyna. Remove this line to keep local edits."

// SliceUnit keeps agent CPU and I/O below foreground services.
// An idle machine still gives agents every core.
const SliceUnit = managedHeader + `
[Unit]
Description=Dyna agent work

[Slice]
CPUWeight=20
IOWeight=20
MemoryHigh=65%
MemoryMax=80%
`

var (
	lookPath   = exec.LookPath
	runCommand = func(name string, args ...string) error { return childenv.Clean(exec.Command(name, args...)).Run() }
	sliceOnce  sync.Once
	// underTest keeps Go tests out of the real user manager.
	underTest = testing.Testing()
)

// Wrap moves cmd into a new scope below dyna.slice and returns the scope's
// unit name. name labels the scope, for example a session ID; it shows in
// systemd-cgtop and systemctl status. The process keeps its PID, environment
// and inherited files. Wrap does nothing and returns "" when the systemd
// user manager is not available.
func Wrap(cmd *exec.Cmd, name string) string {
	if strings.EqualFold(os.Getenv("DYNA_SLICE"), "off") {
		return ""
	}
	return wrap(cmd, name, Slice)
}

// wrap uses the same scope command for agent and foreground processes. It
// returns the scope's unit name, or "" when it left cmd unchanged.
func wrap(cmd *exec.Cmd, name, slice string, properties ...string) string {
	run, ok := systemdRun()
	if !ok {
		return ""
	}
	if slice == Slice {
		sliceOnce.Do(ensureSlice)
	}
	unit := unitName(name)
	args := []string{run, "--user", "--scope", "--quiet", "--collect",
		"--expand-environment=no", "--slice=" + slice,
		"--unit=" + unit, "--description=Dyna " + name}
	for _, property := range properties {
		args = append(args, "--property="+property)
	}
	args = append(args, "--")
	cmd.Args = append(args, append([]string{cmd.Path}, cmd.Args[1:]...)...)
	cmd.Path = run
	return unit
}

func systemdRun() (string, bool) {
	if underTest || runtime.GOOS != "linux" {
		return "", false
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", false
	}
	// The private socket exists only while the user manager runs, so
	// systemd-run will not fail and drop the command.
	if _, err := os.Stat(filepath.Join(dir, "systemd", "private")); err != nil {
		return "", false
	}
	run, err := lookPath("systemd-run")
	return run, err == nil
}

// ensureSlice installs or refreshes the managed slice file and reloads the
// user manager when it changed. Errors leave the default slice settings.
func ensureSlice() {
	config, err := os.UserConfigDir()
	if err != nil {
		return
	}
	changed, err := WriteSlice(filepath.Join(config, "systemd", "user"))
	if err == nil && changed {
		_ = runCommand("systemctl", "--user", "daemon-reload")
	}
}

// WriteSlice installs the managed agent policy without contacting systemd.
// An unmarked slice keeps the user's local resource settings.
func WriteSlice(dir string) (bool, error) {
	path := filepath.Join(dir, Slice)
	old, err := os.ReadFile(path)
	if err == nil && (bytes.Equal(old, []byte(SliceUnit)) || !bytes.HasPrefix(old, []byte(managedHeader))) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, err
	}
	if err := fsutil.WriteFile(path, []byte(SliceUnit), 0644, true); err != nil {
		return false, err
	}
	return true, nil
}

var unsafeUnitChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// unitName returns a unique scope name. The random suffix keeps a retried
// turn from colliding with a scope that a leftover background process keeps
// alive.
func unitName(name string) string {
	name = strings.Trim(unsafeUnitChars.ReplaceAllString(name, "-"), "-.")
	if len(name) > 120 {
		name = name[:120]
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	if name == "" {
		return "dyna-" + hex.EncodeToString(b[:]) + ".scope"
	}
	return "dyna-" + name + "-" + hex.EncodeToString(b[:]) + ".scope"
}
