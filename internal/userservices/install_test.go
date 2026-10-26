package userservices

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
)

func environment(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/temporary/fake-bus")
	return Dir()
}

func TestServiceInstallationAndTransientTransition(t *testing.T) {
	for _, transient := range []bool{false, true} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("transient=%v/changed=%v", transient, changed), func(t *testing.T) {
				dir := environment(t)
				var calls []string
				run := func(_ context.Context, args ...string) (string, error) {
					command := strings.Join(args, " ")
					calls = append(calls, command)
					if strings.Contains(command, " show ") {
						value := "no"
						if transient {
							value = "yes"
						}
						return fmt.Sprintf("Transient=%s\nActiveState=active\nLoadState=loaded\n", value), nil
					}
					return "", nil
				}
				o := Options{Binary: "/test/bin/dyna", Dir: dir, RestartComputers: changed, Run: run, Environment: []string{"DYNA_COMPUTERS_ENABLED=0", "DYNA_OWNER_DISPLAY=:9", "DYNA_OWNER_WAYLAND_DISPLAY=owner-display", "DYNA_COMPUTER_MEMORY_HIGH=123456", "PATH=/test/tools", "DBUS_SESSION_BUS_ADDRESS=unix:path=/fake-bus"}}
				// Repeat while the old transient unit is active. Installing its file
				// must not turn the next landing into a destructive restart either.
				for range 2 {
					if err := Install(context.Background(), o); err != nil {
						t.Fatal(err)
					}
				}
				all := strings.Join(calls, "\n")
				wantRestart := changed && !transient
				if got := strings.Contains(all, "try-restart "+Computers); got != wantRestart {
					t.Fatalf("computer restart=%v, want %v: %s", got, wantRestart, all)
				}
				if strings.Contains(all, "stop "+Computers) || strings.Contains(all, " enable ") {
					t.Fatalf("unsafe transient transition: %s", all)
				}
				if !strings.Contains(all, "daemon-reload") || !strings.Contains(all, "restart "+Engine) {
					t.Fatal(all)
				}
				for _, name := range []string{Engine, Computers} {
					if target, err := os.Readlink(filepath.Join(dir, "default.target.wants", name)); err != nil || target != "../"+name {
						t.Fatalf("unit not enabled: %s %s %v", name, target, err)
					}
				}
				engine, err := os.ReadFile(filepath.Join(dir, Engine))
				if err != nil {
					t.Fatal(err)
				}
				for _, text := range []string{"Wants=network-online.target dyna-computers.service", "After=network-online.target dyna-computers.service", `ExecStart="/test/bin/dyna" engine run --allow-agent`, "TimeoutStopSec=25"} {
					if !strings.Contains(string(engine), text) {
						t.Fatalf("missing %q: %s", text, engine)
					}
				}
				computer, err := os.ReadFile(filepath.Join(dir, Computers))
				if err != nil {
					t.Fatal(err)
				}
				for _, text := range []string{`ExecStart="/test/bin/dyna" computers`, "Restart=on-failure", "Slice=dyna.slice", "Delegate=yes", `EnvironmentFile=-"%E/systemd/user/dyna-computers.env"`} {
					if !strings.Contains(string(computer), text) {
						t.Fatalf("missing %q: %s", text, computer)
					}
				}
				if strings.Contains(string(computer), "PartOf=") || strings.Contains(string(computer), "BindsTo=") {
					t.Fatal("computer lifetime is tied to the engine")
				}
				// The unit must outlast the engine's own stop wait. 25 seconds is the
				// 10 seconds the engine waits for local CI runs plus 15 of grace.
				if engineStopTimeout() <= localci.StopWait {
					t.Fatalf("unit stop timeout %s does not cover the stop wait %s", engineStopTimeout(), localci.StopWait)
				}
				config, err := os.ReadFile(filepath.Join(dir, "dyna-computers.env"))
				if err != nil {
					t.Fatal(err)
				}
				for _, text := range []string{`DYNA_COMPUTERS_ENABLED="0"`, `DYNA_OWNER_DISPLAY=":9"`, `DYNA_COMPUTER_MEMORY_HIGH="123456"`, `DYNA_OWNER_WAYLAND_DISPLAY="owner-display"`, `PATH="/test/tools"`, `DBUS_SESSION_BUS_ADDRESS="unix:path=/fake-bus"`} {
					if !strings.Contains(string(config), text) {
						t.Fatalf("missing environment %q: %s", text, config)
					}
				}
			})
		}
	}
}

func TestInstallInspectionFailurePreservesFiles(t *testing.T) {
	dir := environment(t)
	want := errors.New("fake manager unavailable")
	err := Install(context.Background(), Options{Binary: "/test/dyna", Dir: dir, Run: func(context.Context, ...string) (string, error) { return "", want }})
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, Engine)); !os.IsNotExist(err) {
		t.Fatal("changed units despite unknown live service state")
	}
}

func TestComputerEnvironmentPreservesAbsentSettings(t *testing.T) {
	dir := environment(t)
	if err := ComputerEnvironment(dir, []string{`DYNA_OWNER_DISPLAY=:1`, `PATH=/test tools/100%`, `DYNA_OWNER_WAYLAND_DISPLAY=a"b\c`}); err != nil {
		t.Fatal(err)
	}
	if err := ComputerEnvironment(dir, []string{"PATH=/new/tools"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "dyna-computers.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`DYNA_OWNER_DISPLAY=":1"`, `PATH="/new/tools"`, `DYNA_OWNER_WAYLAND_DISPLAY="a\"b\\c"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q: %s", want, data)
		}
	}
}

func TestInstallRefusesRealRunnerInGoTests(t *testing.T) {
	dir := environment(t)
	if err := Install(context.Background(), Options{Binary: "/test/dyna", Dir: dir}); err == nil || !strings.Contains(err.Error(), "Go tests") {
		t.Fatalf("installer guard: %v", err)
	}
}

func TestWriteLeavesImageServicesDisabled(t *testing.T) {
	dir := environment(t)
	if err := Write(Options{Binary: "/usr/local/bin/dyna", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{Engine, Computers} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "default.target.wants")); !os.IsNotExist(err) {
		t.Fatal("write-only enabled image services")
	}
}

// The engine restart can take longer than systemctl's client bound while the
// engine drains. A failed restart command is not a failed install when systemd
// has started the engine again.
func TestEngineRestartFailureIsOnlyReportedWhenTheEngineDoesNotComeBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		states  []string // answers to successive "show dyna-engine" calls
		wantErr bool
	}{
		{"back under a new invocation", []string{"ActiveState=active\nInvocationID=old\n", "ActiveState=deactivating\nInvocationID=old\n", "ActiveState=active\nInvocationID=new\n"}, false},
		{"still the old process", []string{"ActiveState=active\nInvocationID=old\n"}, true},
		{"failed to start", []string{"ActiveState=active\nInvocationID=old\n", "ActiveState=failed\nInvocationID=new\n"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := environment(t)
			shows, restarts := 0, 0
			run := func(_ context.Context, args ...string) (string, error) {
				command := strings.Join(args, " ")
				switch {
				case strings.Contains(command, "show "+Computers):
					return "Transient=no\nActiveState=active\nLoadState=loaded\n", nil
				case strings.Contains(command, "show "+Engine):
					state := tc.states[min(shows, len(tc.states)-1)]
					shows++
					return state, nil
				case strings.Contains(command, "restart "+Engine):
					restarts++
					return "", context.DeadlineExceeded
				}
				return "", nil
			}
			pauses := 0
			o := Options{Binary: "/test/bin/dyna", Dir: dir, Run: run, Environment: []string{"PATH=/test/tools"},
				Pause: func(context.Context, time.Duration) error { pauses++; return nil }}
			err := Install(context.Background(), o)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if restarts != 1 {
				t.Fatalf("restart ran %d times", restarts)
			}
			if tc.wantErr && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("the restart error was lost: %v", err)
			}
			if pauses > engineChecks {
				t.Fatalf("paused %d times, bound is %d", pauses, engineChecks)
			}
		})
	}
}

// The install reports an engine that restarts but is not healthy, for
// example one whose migration failed and that now runs read-only.
// The tunnel is its own unit, so an engine restart does not drop the connector
// and the remote app keeps a way back in.
func TestWriteInstallsTheTunnelUnit(t *testing.T) {
	dir := t.TempDir()
	if err := Write(Options{Binary: "/opt/dyna/bin/dyna", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, Tunnel))
	if err != nil {
		t.Fatalf("no tunnel unit: %v", err)
	}
	for _, want := range []string{
		`ExecStart="/opt/dyna/bin/dyna" engine tunnel`,
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("tunnel unit lacks %q:\n%s", want, body)
		}
	}
	// The engine orders itself after the connector it now depends on.
	engineUnitBody, err := os.ReadFile(filepath.Join(dir, Engine))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(engineUnitBody), "After=network-online.target dyna-computers.service dyna-tunnel.service") {
		t.Fatalf("the engine unit does not order itself after the tunnel unit:\n%s", engineUnitBody)
	}
}

// An engine that opted out of the tunnel gets no connector: no unit file, no
// enable link, and an earlier connector is stopped.
func TestInstallHonoursTheTunnelOptOut(t *testing.T) {
	dir := t.TempDir()
	before := Options{Binary: "/test/bin/dyna", Dir: dir}
	if err := Write(before); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, Tunnel)); err != nil {
		t.Fatalf("fixture has no tunnel unit: %v", err)
	}
	var calls []string
	run := func(_ context.Context, args ...string) (string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		switch {
		case strings.Contains(command, " show "+Tunnel):
			// The unit is installed, so the installer must stop it here, before
			// Write removes the file.
			if _, err := os.Stat(filepath.Join(dir, Tunnel)); err != nil {
				return "LoadState=not-found\n", errors.New("unit is gone")
			}
			return "LoadState=loaded\nActiveState=active\n", nil
		case strings.Contains(command, " show "):
			return "Transient=no\nActiveState=active\nLoadState=loaded\n", nil
		case command == "systemctl --user disable --now "+Tunnel:
			// systemctl refuses this once the file is gone.
			if _, err := os.Stat(filepath.Join(dir, Tunnel)); err != nil {
				return "Unit file does not exist.\n", errors.New("exit status 1")
			}
		}
		return "", nil
	}
	if err := Install(context.Background(), Options{Binary: "/test/bin/dyna", Dir: dir, NoTunnel: true, Run: run}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, Tunnel)); !os.IsNotExist(err) {
		t.Fatalf("the tunnel unit stayed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "default.target.wants", Tunnel)); !os.IsNotExist(err) {
		t.Fatalf("the tunnel unit stayed enabled: %v", err)
	}
	all := strings.Join(calls, "\n")
	if strings.Contains(all, "start "+Tunnel) {
		t.Fatalf("an opted-out install started a connector: %s", all)
	}
	// The fake refuses to disable a unit whose file is already gone, so reaching
	// this point proves the stop came before Write removed the unit.
	if !strings.Contains(all, "disable --now "+Tunnel) {
		t.Fatalf("an opted-out install did not stop an earlier connector: %s", all)
	}
	engine, err := os.ReadFile(filepath.Join(dir, Engine))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(engine), `ExecStart="/test/bin/dyna" engine run --allow-agent --no-tunnel`) {
		t.Fatalf("the opt-out is not in the engine unit:\n%s", engine)
	}
	// A later install keeps the choice the engine unit records.
	var again []string
	keep := func(_ context.Context, args ...string) (string, error) {
		command := strings.Join(args, " ")
		again = append(again, command)
		if strings.Contains(command, " show ") {
			return "Transient=no\nActiveState=active\nLoadState=loaded\n", nil
		}
		return "", nil
	}
	if err := Install(context.Background(), Options{Binary: "/test/bin/dyna", Dir: dir, Run: keep}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(again, "\n"), "start "+Tunnel) {
		t.Fatalf("a second install lost the owner's opt-out: %v", again)
	}
}

// A written unit is not a running connector: the write-only install leaves the
// service inactive, so the engine keeps its own connector.
func TestWrittenTunnelUnitIsNotRunning(t *testing.T) {
	dir := t.TempDir()
	if err := Write(Options{Binary: "/test/bin/dyna", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, Tunnel)); err != nil {
		t.Fatalf("fixture has no tunnel unit: %v", err)
	}
	inactive := func(context.Context, ...string) (string, error) { return "ActiveState=inactive\n", nil }
	if tunnelActive(context.Background(), inactive) {
		t.Fatal("a written but inactive unit counts as a running connector")
	}
	failed := func(context.Context, ...string) (string, error) { return "", errors.New("no user manager") }
	if tunnelActive(context.Background(), failed) {
		t.Fatal("a failed query counts as a running connector")
	}
	active := func(context.Context, ...string) (string, error) { return "ActiveState=active\n", nil }
	if !tunnelActive(context.Background(), active) {
		t.Fatal("a running connector was not seen")
	}
}

func TestInstallChecksEngineHealthAfterTheRestart(t *testing.T) {
	run := func(_ context.Context, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "show "+Computers) {
			return "Transient=no\nActiveState=active\nLoadState=loaded\n", nil
		}
		return "", nil
	}
	sick := errors.New("the engine is up, but read-only: migrate dyna.db: boom")
	for _, tc := range []struct {
		name    string
		healthy error
	}{{"healthy", nil}, {"not healthy", sick}} {
		t.Run(tc.name, func(t *testing.T) {
			checked := false
			o := Options{Binary: "/test/bin/dyna", Dir: environment(t), Run: run, Environment: []string{"PATH=/test/tools"},
				Healthy: func(ctx context.Context) error {
					if _, ok := ctx.Deadline(); !ok {
						t.Error("the health check has no bound")
					}
					checked = true
					return tc.healthy
				}}
			err := Install(context.Background(), o)
			if !checked || !errors.Is(err, tc.healthy) || (tc.healthy == nil) != (err == nil) {
				t.Fatalf("checked %v, err %v; want health checked and error %v", checked, err, tc.healthy)
			}
		})
	}
}

func TestComputerRestartWaitsForQuiet(t *testing.T) {
	for name, tc := range map[string]struct {
		changed        bool
		busy           string
		wantRestart    bool
		wantPending    bool
		wantOutputPart string
	}{
		"changed and quiet": {true, "", true, false, "Restarted " + Computers},
		"changed and busy":  {true, "an app is open in room x", false, true, "Deferred restart of " + Computers + ": an app is open in room x"},
		"unchanged":         {false, "an app is open", false, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			var calls []string
			var out strings.Builder
			o := Options{Binary: "/test/bin/dyna", Dir: dir, RestartComputers: tc.changed, Output: &out,
				Environment: []string{"PATH=/test/tools"},
				Quiet:       func(context.Context) (string, error) { return tc.busy, nil },
				Run: func(_ context.Context, args ...string) (string, error) {
					calls = append(calls, strings.Join(args, " "))
					return "Transient=no\nLoadState=loaded\n", nil
				}}
			if err := Install(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(strings.Join(calls, "\n"), "try-restart "+Computers); got != tc.wantRestart {
				t.Fatalf("restart=%v want %v: %v", got, tc.wantRestart, calls)
			}
			if !strings.Contains(strings.Join(calls, "\n"), "systemctl --user restart "+Engine) {
				t.Fatalf("engine restart was deferred: %v", calls)
			}
			if RestartIsPending(dir) != tc.wantPending {
				t.Fatalf("pending=%v want %v", RestartIsPending(dir), tc.wantPending)
			}
			if !strings.Contains(out.String(), tc.wantOutputPart) {
				t.Fatalf("output missing %q: %s", tc.wantOutputPart, out.String())
			}
		})
	}
}

// TestDefaultRunnerRunsSystemctlOnce keeps the Options.Run contract: the runner
// gets the full argv that runWithin builds, so systemctl runs once, not once per
// level. A doubled prefix made every install fail before it wrote any unit.
func TestDefaultRunnerRunsSystemctlOnce(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\nprintf 'Transient=no\\nActiveState=active\\nLoadState=loaded\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	out, err := systemctlRunner(os.Environ())(context.Background(), "systemctl", "--user", "show", Computers, "--property=Transient,ActiveState,LoadState")
	if err != nil {
		t.Fatalf("runner: %v (%s)", err, out)
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want := "--user\nshow\n" + Computers + "\n--property=Transient,ActiveState,LoadState\n"
	if string(got) != want {
		t.Fatalf("argv =\n%s\nwant\n%s", got, want)
	}
}

func TestRestartPendingSince(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	window := 30 * time.Minute
	mark := func(age time.Duration) {
		t.Helper()
		if err := MarkRestartPending(dir); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(filepath.Join(dir, RestartPending), at, at); err != nil {
			t.Fatal(err)
		}
	}
	started := now.Add(-time.Hour)
	if RestartPendingSince(dir, started, now, window) {
		t.Fatal("no marker counted as pending")
	}
	mark(10 * time.Minute)
	if !RestartPendingSince(dir, started, now, window) {
		t.Fatal("a recent marker after the service started was ignored")
	}
	if RestartPendingSince(dir, now.Add(-5*time.Minute), now, window) {
		t.Fatal("a marker older than the service start still blocks: the restart already happened")
	}
	mark(time.Hour)
	if RestartPendingSince(dir, now.Add(-2*time.Hour), now, window) {
		t.Fatal("a marker older than the window blocks for good")
	}
}
