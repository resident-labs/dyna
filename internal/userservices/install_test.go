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
				for _, text := range []string{"Wants=network-online.target dyna-computers.service", "After=network-online.target dyna-computers.service", `ExecStart="/test/bin/dyna" engine run --allow-agent`} {
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
