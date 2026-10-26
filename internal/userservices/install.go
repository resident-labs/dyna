// Package userservices installs the engine and its independent computer service.
package userservices

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/localci"
)

const Engine = agentslice.EngineUnit
const Computers = "dyna-computers.service"

// Tunnel runs the managed cloudflared connector. It is its own unit, so an
// engine restart does not drop the tunnel and the remote app stays reachable
// while the new engine starts.
const Tunnel = "dyna-tunnel.service"

type Options struct {
	Binary           string
	Dir              string
	Environment      []string
	RestartComputers bool
	// NoTunnel keeps the owner's tunnel opt-out: no connector unit is installed,
	// and an installed one is stopped. Install keeps the setting an existing
	// engine unit already carries.
	NoTunnel bool
	// Quiet returns why restarting the computer service would disturb users, or
	// "" when it is safe. Nil means always safe.
	Quiet  func(context.Context) (string, error)
	Run    func(context.Context, ...string) (string, error)
	Output io.Writer
	// Pause waits between engine state checks after a failed restart. Tests set it
	// to return at once.
	Pause func(context.Context, time.Duration) error
	// Healthy checks the restarted engine, for example through its Health
	// call. An install whose engine is not healthy fails. Nil skips the check.
	Healthy func(context.Context) error
}

// RestartPending is the marker an install leaves in the unit directory when it
// defers a computer service restart. The engine restarts the service at the
// first quiet moment and removes the marker.
const RestartPending = "dyna-computers.restart-pending"

func MarkRestartPending(dir string) error {
	return fsutil.WriteFile(filepath.Join(dir, RestartPending), []byte("1\n"), 0600, true)
}

func RestartIsPending(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, RestartPending))
	return err == nil
}

// RestartPendingSince reports a deferred restart that the service started at
// started has not applied: the marker is newer than started and younger than
// window at now. The window keeps a marker that nothing clears from holding the
// service back for good.
func RestartPendingSince(dir string, started, now time.Time, window time.Duration) bool {
	st, err := os.Stat(filepath.Join(dir, RestartPending))
	if err != nil {
		return false
	}
	return st.ModTime().After(started) && now.Sub(st.ModTime()) < window
}

// RestartPendingAt is when the install left the restart marker, which is when
// the restart became pending.
func RestartPendingAt(dir string) (time.Time, bool) {
	st, err := os.Stat(filepath.Join(dir, RestartPending))
	if err != nil {
		return time.Time{}, false
	}
	return st.ModTime(), true
}

func ClearRestartPending(dir string) error {
	if err := os.Remove(filepath.Join(dir, RestartPending)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// TryRestartComputers restarts the computer service if it is running.
func TryRestartComputers(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, commandBound)
	defer cancel()
	out, err := systemctlUser(ctx, os.Environ(), "try-restart", Computers)
	if err != nil {
		return fmt.Errorf("restart %s: %w: %s", Computers, err, out)
	}
	return nil
}

// ComputersActive reports whether the computer service is running or starting.
// A failed query is an error, never "inactive".
func ComputersActive(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, commandBound)
	defer cancel()
	out, err := systemctlUser(ctx, os.Environ(), "show", Computers, "--property=ActiveState")
	if err != nil {
		return false, fmt.Errorf("query %s: %w", Computers, err)
	}
	switch property(out, "ActiveState") {
	case "inactive", "failed":
		return false, nil
	default:
		return true, nil
	}
}

const (
	// systemctl calls are short, except restarting the engine: a stop can take
	// longer than ten seconds while the engine drains.
	commandBound = 10 * time.Second
	restartBound = 60 * time.Second
	// After a restart command fails, the engine may still come up. Check this many
	// times, one second apart, before reporting the failure.
	engineChecks = 30
	// healthBound is how long the restarted engine has to answer Health. It
	// covers a few systemd restarts (RestartSec=3) of an engine that dies.
	healthBound = 30 * time.Second
)

// Dir uses the same XDG location as the install hook's captured configuration.
func Dir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "systemd", "user")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

// TunnelInstalled is gone: whether the connector runs is a question for
// systemd, not for the presence of a unit file. Write leaves a file behind
// without starting anything, and a failed install can leave one too.

// TunnelActive reports whether the managed tunnel service is running. The engine
// leaves its own connector to that service only when this proves it is up, so a
// written or leftover unit file never leaves remote access without a connector.
func TunnelActive(ctx context.Context) bool {
	env := os.Environ()
	return tunnelActive(ctx, func(ctx context.Context, argv ...string) (string, error) {
		return systemctlUser(ctx, env, argv[1:]...)
	})
}

func tunnelActive(ctx context.Context, run func(context.Context, ...string) (string, error)) bool {
	ctx, cancel := context.WithTimeout(ctx, commandBound)
	defer cancel()
	out, err := run(ctx, "systemctl", "show", Tunnel, "--property=ActiveState")
	if err != nil {
		return false
	}
	switch property(out, "ActiveState") {
	case "active", "activating", "reloading":
		return true
	}
	return false
}

func quote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "%", "%%").Replace(value) + `"`
}

// engineStopGrace is added to the engine's own stop budget for the unit's stop
// deadline, so systemd gives the engine room to exit after its last cleanup.
const engineStopGrace = 15 * time.Second

// engineStopTimeout is the unit's stop deadline. An engine that stops waits for
// its local CI runs to stop polling, which is localci.StopWait; their checks run
// detached on the machines and keep running.
func engineStopTimeout() time.Duration { return localci.StopWait + engineStopGrace }

func engineUnit(binary string, noTunnel bool) string {
	run := ` engine run --allow-agent`
	if noTunnel {
		run += " --no-tunnel"
	}
	return `[Unit]
Description=Dyna engine
After=network-online.target dyna-computers.service dyna-tunnel.service
Wants=network-online.target dyna-computers.service

[Service]
ExecStart=` + quote(binary) + run + `
Restart=always
RestartSec=3
KillMode=process
Delegate=yes
TimeoutStopSec=` + strconv.Itoa(int(engineStopTimeout()/time.Second)) + `
Slice=` + agentslice.ForegroundSlice + `
CPUWeight=` + agentslice.EngineWeight + `
IOWeight=` + agentslice.EngineWeight + `

[Install]
WantedBy=default.target
`
}

func tunnelUnit(binary string) string {
	return `[Unit]
Description=Dyna tunnel connector
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + quote(binary) + ` engine tunnel
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`
}

func computersUnit(binary string) string {
	return `[Unit]
Description=Dyna computer supervisor

[Service]
Type=exec
ExecStart=` + quote(binary) + ` computers
EnvironmentFile=-"%E/systemd/user/dyna-computers.env"
Restart=on-failure
RestartSec=3
Delegate=yes
Slice=dyna.slice

[Install]
WantedBy=default.target
`
}

// ComputerEnvironment keeps the settings the transient starter used to forward.
// Missing settings retain their installed values across sanitized hook launches.
func ComputerEnvironment(dir string, env []string) error {
	path := filepath.Join(dir, "dyna-computers.env")
	previous, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(previous), "\n") {
		if key, _, ok := strings.Cut(line, "="); ok {
			values[key] = line
		}
	}
	current := map[string]string{}
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			current[key] = value
		}
	}
	var lines []string
	for _, key := range []string{"PATH", "HOME", "XDG_RUNTIME_DIR", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "DBUS_SESSION_BUS_ADDRESS", "DYNA_COMPUTERS_ENABLED", "DYNA_OWNER_WAYLAND_DISPLAY", "DYNA_OWNER_DISPLAY", "HYPRLAND_INSTANCE_SIGNATURE", "DYNA_COMPUTER_MEMORY_HIGH"} {
		if value, ok := current[key]; ok {
			if strings.ContainsAny(value, "\x00\r\n") {
				return fmt.Errorf("computer environment %s contains a line break or NUL", key)
			}
			values[key] = key + "=" + `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
		}
		if line, ok := values[key]; ok {
			lines = append(lines, line)
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return fsutil.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600, true)
}

// engineOptsOut reports whether the installed engine unit runs with --no-tunnel.
// The owner set it, and an install must not start a connector against it.
func engineOptsOut(dir string) bool {
	body, err := os.ReadFile(filepath.Join(dir, Engine))
	return err == nil && strings.Contains(string(body), "--no-tunnel")
}

// Write writes the shared unit definitions without contacting systemd or enabling
// units. The image build uses it before a user manager exists.
func Write(o Options) error {
	if testing.Testing() && o.Dir == "" {
		return fmt.Errorf("service unit writes in Go tests require an explicit temporary directory")
	}
	if !filepath.IsAbs(o.Binary) {
		return fmt.Errorf("service binary must be an absolute path")
	}
	if o.Dir == "" {
		o.Dir = Dir()
	}
	if !o.NoTunnel && engineOptsOut(o.Dir) {
		// Keep the choice an earlier install wrote, instead of rewriting it away.
		o.NoTunnel = true
	}
	if err := os.MkdirAll(o.Dir, 0755); err != nil {
		return err
	}
	if _, err := agentslice.WriteSlice(o.Dir); err != nil {
		return err
	}
	units := map[string]string{Engine: engineUnit(o.Binary, o.NoTunnel), Computers: computersUnit(o.Binary)}
	if !o.NoTunnel {
		units[Tunnel] = tunnelUnit(o.Binary)
	}
	for name, text := range units {
		if err := fsutil.WriteFile(filepath.Join(o.Dir, name), []byte(text), 0644, true); err != nil {
			return err
		}
	}
	if o.NoTunnel {
		// An engine that opted out must not keep the connector of an earlier
		// install around.
		if err := os.Remove(filepath.Join(o.Dir, Tunnel)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Install preserves a transient computer service until it stops naturally.
// Ordinary engine restarts never restart its independent computer dependency.
func Install(ctx context.Context, o Options) error {
	if o.Run == nil && testing.Testing() {
		return fmt.Errorf("refusing service installation from Go tests; use a fake runner")
	}
	if o.Dir == "" {
		o.Dir = Dir()
	}
	if !o.NoTunnel && engineOptsOut(o.Dir) {
		// Keep the choice an earlier install wrote, instead of rewriting it away.
		o.NoTunnel = true
	}
	if o.Environment == nil {
		o.Environment = os.Environ()
	}
	if o.Output == nil {
		o.Output = io.Discard
	}
	command := o.Run
	if command == nil {
		command = systemctlRunner(o.Environment)
	}
	runWithin := func(bound time.Duration, args ...string) (string, error) {
		fmt.Fprintln(o.Output, "$ systemctl --user "+strings.Join(args, " "))
		commandCtx, cancel := context.WithTimeout(ctx, bound)
		defer cancel()
		output, err := command(commandCtx, append([]string{"systemctl", "--user"}, args...)...)
		fmt.Fprint(o.Output, output)
		return output, err
	}
	run := func(args ...string) (string, error) { return runWithin(commandBound, args...) }
	state, err := run("show", Computers, "--property=Transient,ActiveState,LoadState")
	if err != nil && !strings.Contains(state, "LoadState=not-found") {
		return fmt.Errorf("inspect computer service before installation: %w", err)
	}
	// TODO: legacy/shim remove later — preserves the pre-unit transient computer service until it stops and is collected; remove after all installs use the persistent unit.
	transient := strings.Contains(state, "Transient=yes")
	// Stop an opted-out connector before Write removes its unit, so the service
	// still exists when systemctl is asked to stop it. A machine that never had
	// one is already done.
	if o.NoTunnel {
		state, err := run("show", Tunnel, "--property=LoadState,ActiveState")
		if err != nil && !strings.Contains(state, "LoadState=not-found") {
			return fmt.Errorf("inspect tunnel service before removal: %w", err)
		}
		if !strings.Contains(state, "LoadState=not-found") {
			if _, err := run("disable", "--now", Tunnel); err != nil {
				return err
			}
		}
	}
	if err := retireEngineRestart(ctx, o.Dir, run); err != nil {
		return err
	}
	if err := Write(o); err != nil {
		return err
	}
	if err := ComputerEnvironment(o.Dir, o.Environment); err != nil {
		return err
	}
	// Enable the actual on-disk files. enable-by-name can resolve the transient
	// unit instead, which has no install section and cannot be enabled.
	wants := filepath.Join(o.Dir, "default.target.wants")
	if err := os.MkdirAll(wants, 0755); err != nil {
		return err
	}
	for _, name := range []string{Engine, Computers, Tunnel} {
		path := filepath.Join(wants, name)
		if o.NoTunnel && name == Tunnel {
			// An engine that opted out enables no connector.
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if target, err := os.Readlink(path); err == nil && target == "../"+name {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Symlink("../"+name, path); err != nil {
			return err
		}
	}
	// TODO: legacy/shim remove later — migrates the old dyna-serve unit and reports its flags; remove once every install runs dyna-engine.
	if old, err := os.ReadFile(filepath.Join(o.Dir, "dyna-serve.service")); err == nil {
		names := map[string]bool{}
		for _, flag := range regexp.MustCompile("--[a-z][a-z-]*").FindAllString(string(old), -1) {
			names[flag] = true
		}
		var flags []string
		for flag := range names {
			flags = append(flags, flag)
		}
		sort.Strings(flags)
		if len(flags) > 0 {
			fmt.Fprintf(o.Output, "The old service used %s; pass these flags to dyna engine run when you configure dyna-engine.service.\n", strings.Join(flags, " "))
		}
		_, _ = run("disable", "--now", "dyna-serve")
		if err := os.Remove(filepath.Join(o.Dir, "dyna-serve.service")); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := run("daemon-reload"); err != nil {
		return err
	}
	// The connector now runs as its own unit. Start it before the engine restarts,
	// so remote access never waits for the new engine, and the old engine's own
	// connector leaves with that restart. An opted-out install stopped an earlier
	// connector before Write removed its unit.
	if !o.NoTunnel {
		if _, err := run("start", Tunnel); err != nil {
			return err
		}
	}
	if transient {
		fmt.Fprintln(o.Output, "Keeping the transient computer service and its live desktops; the persistent unit applies after it stops.")
	} else if o.RestartComputers {
		busy := ""
		if o.Quiet != nil {
			if busy, err = o.Quiet(ctx); err != nil {
				return fmt.Errorf("check computer activity: %w", err)
			}
		}
		if busy != "" {
			if err := MarkRestartPending(o.Dir); err != nil {
				return err
			}
			fmt.Fprintf(o.Output, "Deferred restart of %s: %s. The engine restarts it at the first quiet moment.\n", Computers, busy)
		} else {
			if _, err := run("try-restart", Computers); err != nil {
				return err
			}
			if err := ClearRestartPending(o.Dir); err != nil {
				return err
			}
			fmt.Fprintf(o.Output, "Restarted %s.\n", Computers)
		}
	}
	if err := restartEngine(ctx, o, run, runWithin); err != nil {
		return err
	}
	if o.Healthy == nil {
		return nil
	}
	healthCtx, cancel := context.WithTimeout(ctx, healthBound)
	defer cancel()
	if err := o.Healthy(healthCtx); err != nil {
		return fmt.Errorf("%s restarted but is not healthy: %w (see journalctl --user -u %s)", Engine, err, Engine)
	}
	fmt.Fprintf(o.Output, "%s answers Health.\n", Engine)
	return nil
}

// restartEngine restarts the engine and waits for systemd to finish the job. A
// restart command that fails or times out says nothing about the engine: systemd
// keeps working on the job after the client is killed. So a failure is reported
// only when the engine has not started again within a bound.
func restartEngine(ctx context.Context, o Options, run func(...string) (string, error), runWithin func(time.Duration, ...string) (string, error)) error {
	const properties = "--property=ActiveState,InvocationID"
	before, _ := run("show", Engine, properties)
	_, err := runWithin(restartBound, "restart", Engine)
	if err == nil {
		return nil
	}
	pause := o.Pause
	if pause == nil {
		pause = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	// Every start gets a new InvocationID, so an engine that is active under a new
	// one has been restarted, and one that is still the old one has not.
	was := property(before, "InvocationID")
	for range engineChecks {
		state, _ := run("show", Engine, properties)
		switch property(state, "ActiveState") {
		case "active":
			if now := property(state, "InvocationID"); now != "" && now != was {
				fmt.Fprintf(o.Output, "The restart command failed (%v), but %s is running again.\n", err, Engine)
				return nil
			}
		case "failed":
			return err
		}
		if pause(ctx, time.Second) != nil {
			break
		}
	}
	return err
}

// systemctlUser runs one systemctl --user command with the given environment,
// so the engine and an install reach the user manager the same way.
func systemctlUser(ctx context.Context, env []string, args ...string) (string, error) {
	return systemctlRunner(env)(ctx, append([]string{"systemctl", "--user"}, args...)...)
}

// systemctlRunner is the default Options.Run. It runs the argv that runWithin
// builds, which already names systemctl and --user, so a replacement runner
// sees the same argv.
func systemctlRunner(env []string) func(context.Context, ...string) (string, error) {
	return func(ctx context.Context, argv ...string) (string, error) {
		if len(argv) == 0 {
			return "", errors.New("systemctl runner needs a command")
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = env
		output, err := childenv.Clean(cmd).CombinedOutput()
		return string(output), err
	}
}

// property reads one KEY=value line of systemctl show output.
func property(output, key string) string {
	for _, line := range strings.Split(output, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return value
		}
	}
	return ""
}
