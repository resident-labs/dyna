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
	"context"
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
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/systemdver"
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

// commandTimeout bounds each systemd command, so a stalled user manager cannot
// hold a caller such as a short version probe or the engine's shutdown.
const commandTimeout = 3 * time.Second

var (
	lookPath   = exec.LookPath
	runCommand = func(ctx context.Context, name string, args ...string) error {
		return childenv.Clean(exec.CommandContext(ctx, name, args...)).Run()
	}
	// runCommandOutput reads what a command prints, for the systemd version that
	// decides the scope arguments.
	runCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return childenv.Clean(exec.CommandContext(ctx, name, args...)).Output()
	}
	sliceOnce sync.Once
	// noExpandOnce and noExpand cache what this systemd-run supports. Every agent
	// start asks, and a systemd upgrade comes with a reboot, so one read per
	// process is enough.
	noExpandOnce sync.Once
	noExpand     string
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

// Stop kills every process left in the scope that Wrap returned, including a
// daemon that left the command's process group. It ignores a scope that has
// already ended, and does nothing for "".
func Stop(unit string) {
	if unit == "" {
		return
	}
	// Cleanup has its own bound, not the probe's context, so a probe that was
	// canceled still gets its scope killed, and a stalled user manager cannot
	// hold the caller.
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	_ = runCommand(ctx, "systemctl", "--user", "kill", "--signal=SIGKILL", unit)
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
	args := []string{run, "--user", "--scope", "--quiet", "--collect"}
	if flag := noExpandArg(run); flag != "" {
		args = append(args, flag)
	}
	args = append(args, "--slice="+slice, "--unit="+unit, "--description=Dyna "+name)
	for _, property := range properties {
		args = append(args, "--property="+property)
	}
	args = append(args, "--")
	cmd.Args = append(args, append([]string{cmd.Path}, cmd.Args[1:]...)...)
	cmd.Path = run
	return unit
}

// noExpandArg reads the argument that stops systemd from expanding environment
// variables in the agent command, which holds $ signs of its own. A systemd that
// knows the argument gets it; an older systemd rejects it and expands nothing
// itself, so it runs the scope without it. A systemd version that cannot be read
// keeps the argument, so an old systemd fails loudly instead of running a
// command that systemd rewrote.
func noExpandArg(run string) string {
	noExpandOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		out, err := runCommandOutput(ctx, run, "--version")
		if err != nil {
			noExpand = systemdver.NoExpandFlag
			return
		}
		major, ok := systemdver.Major(string(out))
		if !ok || systemdver.NeedsNoExpand(major) {
			noExpand = systemdver.NoExpandFlag
		}
	})
	return noExpand
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
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		_ = runCommand(ctx, "systemctl", "--user", "daemon-reload")
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
