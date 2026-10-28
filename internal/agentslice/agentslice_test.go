package agentslice

import (
	"context"
	"dyna-agent/internal/testsafe"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeManager makes systemdRun succeed without touching the real user
// manager, and records systemctl calls.
func fakeManager(t *testing.T) (config string, calls *[]string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/temporary/fake-bus")
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "systemd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "systemd", "private"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	config = t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runDir)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("DYNA_SLICE", "")
	underTest = false
	t.Cleanup(func() { underTest = true })
	var got []string
	oldLook, oldRun, oldOutput := lookPath, runCommand, runCommandOutput
	lookPath = func(string) (string, error) { return "/usr/bin/systemd-run", nil }
	runCommand = func(_ context.Context, name string, args ...string) error {
		got = append(got, name+" "+strings.Join(args, " "))
		return nil
	}
	runCommandOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("systemd 262 (262-1-arch)\n"), nil
	}
	sliceOnce, noExpandOnce, noExpand = sync.Once{}, sync.Once{}, ""
	roomSliceMu.Lock()
	roomSliceDone = map[string]bool{}
	roomSliceMu.Unlock()
	noticeOnce, supportOnce, memOnce = sync.Once{}, sync.Once{}, sync.Once{}
	t.Cleanup(func() {
		lookPath, runCommand, runCommandOutput = oldLook, oldRun, oldOutput
		sliceOnce, noExpandOnce, noExpand = sync.Once{}, sync.Once{}, ""
		roomSliceMu.Lock()
		roomSliceDone = map[string]bool{}
		roomSliceMu.Unlock()
		noticeOnce, supportOnce, memOnce = sync.Once{}, sync.Once{}, sync.Once{}
	})
	return config, &got
}

// A systemd before 254 has no --expand-environment argument and rejects it, so
// an agent scope starts without it; from 254 the argument stays, because that
// systemd would otherwise expand the $ signs of the agent command.
func TestWrapSuitsTheSystemdThatRunsIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		version  string
		noExpand bool
	}{
		{"debian 12", "systemd 252 (252.39-1~deb12u1)\n", false},
		{"systemd 254", "systemd 254.5-1\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeManager(t)
			runCommandOutput = func(context.Context, string, ...string) ([]byte, error) { return []byte(tc.version), nil }
			noExpandOnce = sync.Once{}
			cmd := exec.Command("/bin/dyna", "subagent", "_turn", "ses_1", "2")
			Wrap(cmd, "ses_1/2")
			args := strings.Join(cmd.Args, " ")
			if got := strings.Contains(args, "--expand-environment=no"); got != tc.noExpand {
				t.Fatalf("scope args = %q, want the no-expand argument %t", args, tc.noExpand)
			}
			// The agent command reaches systemd-run as written.
			if !strings.Contains(args, "-- /bin/dyna subagent _turn ses_1 2") {
				t.Fatalf("scope args = %q, want the agent command unchanged", args)
			}
		})
	}
}

func TestWrapRewritesCommand(t *testing.T) {
	config, calls := fakeManager(t)
	cmd := exec.Command("/bin/dyna", "subagent", "_turn", "ses_1", "2")
	scope := Wrap(cmd, "ses_1/2")

	if cmd.Path != "/usr/bin/systemd-run" {
		t.Fatalf("Path = %q", cmd.Path)
	}
	args := strings.Join(cmd.Args, " ")
	if !strings.HasPrefix(scope, "dyna-ses_1-2-") || !strings.Contains(args, "--unit="+scope+" ") {
		t.Errorf("Wrap returned %q, which is not the --unit in %q", scope, args)
	}
	for _, want := range []string{"--user --scope", "--slice=dyna.slice", "--unit=dyna-ses_1-2-", "-- /bin/dyna subagent _turn ses_1 2"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	unit, err := os.ReadFile(filepath.Join(config, "systemd", "user", Slice))
	if err != nil || string(unit) != SliceUnit {
		t.Fatalf("slice file = %q, %v", unit, err)
	}
	if len(*calls) != 1 || (*calls)[0] != "systemctl --user daemon-reload" {
		t.Fatalf("calls = %q", *calls)
	}
}

func TestWrapKeepsLocalSliceEdits(t *testing.T) {
	config, calls := fakeManager(t)
	path := filepath.Join(config, "systemd", "user", Slice)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	local := "[Slice]\nCPUWeight=50\n"
	if err := os.WriteFile(path, []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	Wrap(exec.Command("/bin/true"), "x")
	if got, _ := os.ReadFile(path); string(got) != local {
		t.Fatalf("local edit overwritten: %q", got)
	}
	if len(*calls) != 0 {
		t.Fatalf("unexpected reload: %q", *calls)
	}
}

func TestWrapRefreshesManagedSlice(t *testing.T) {
	config, calls := fakeManager(t)
	path := filepath.Join(config, "systemd", "user", Slice)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(managedHeader+"\n[Slice]\nCPUWeight=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Wrap(exec.Command("/bin/true"), "x")
	if got, _ := os.ReadFile(path); string(got) != SliceUnit {
		t.Fatalf("managed slice not refreshed: %q", got)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %q", *calls)
	}
}

func TestWrapDisabled(t *testing.T) {
	fakeManager(t)
	t.Setenv("DYNA_SLICE", "off")
	cmd := exec.Command("/bin/true", "a")
	Wrap(cmd, "x")
	if cmd.Path != "/bin/true" || strings.Join(cmd.Args, " ") != "/bin/true a" {
		t.Fatalf("disabled Wrap changed cmd: %q %q", cmd.Path, cmd.Args)
	}

	t.Setenv("DYNA_SLICE", "")
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t)) // no user manager socket
	Wrap(cmd, "x")
	if cmd.Path != "/bin/true" {
		t.Fatalf("Wrap without a user manager changed cmd: %q", cmd.Path)
	}
}

// TestWrapExec preserves the command contract without a live systemd bus.
func TestWrapExec(t *testing.T) {
	_, calls := fakeManager(t)
	bin := filepath.Join(t.TempDir(), "systemd-run")
	argsPath := filepath.Join(t.TempDir(), "args")
	t.Setenv("SCOPE_ARGS", argsPath)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$SCOPE_ARGS\"\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	lookPath = func(string) (string, error) { return bin, nil }
	f, err := os.CreateTemp(t.TempDir(), "fd")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command("/bin/sh", "-c", "echo $$; readlink /proc/self/fd/3; printf '%s\\n' \"$SCOPE_VALUE\"")
	cmd.Env = append(os.Environ(), "SCOPE_VALUE=kept")
	cmd.ExtraFiles = []*os.File{f}
	Wrap(cmd, "test")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("wrapped command: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 || lines[0] != strconv.Itoa(cmd.Process.Pid) || lines[1] != f.Name() || lines[2] != "kept" {
		t.Fatalf("PID, file or environment changed: %q", out)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil || !strings.Contains(string(args), "--slice=dyna.slice\n") {
		t.Fatalf("scope args: %s, %v", args, err)
	}
	if len(*calls) != 1 {
		t.Fatalf("fake systemctl calls=%v", *calls)
	}
}

func TestAgentSliceHasLowIOWeight(t *testing.T) {
	if !strings.Contains(SliceUnit, "IOWeight=20\n") {
		t.Fatalf("agent I/O weight missing: %s", SliceUnit)
	}
}

// Stop kills what is left in a scope, so a daemon that left the command's
// process group does not stay in the engine's unit.
func TestStopKillsTheScope(t *testing.T) {
	var got []string
	old := runCommand
	runCommand = func(_ context.Context, name string, args ...string) error {
		got = append(got, name+" "+strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { runCommand = old })
	Stop("")
	Stop("dyna-version-probe-1.scope")
	if len(got) != 1 || got[0] != "systemctl --user kill --signal=SIGKILL dyna-version-probe-1.scope" {
		t.Fatalf("commands = %q; want one kill of the scope", got)
	}
}

// Every systemd command has a deadline, so a stalled user manager cannot hold
// the caller.
func TestSystemdCommandsAreBounded(t *testing.T) {
	var deadlines []bool
	old := runCommand
	runCommand = func(ctx context.Context, _ string, _ ...string) error {
		_, ok := ctx.Deadline()
		deadlines = append(deadlines, ok && ctx.Err() == nil)
		return nil
	}
	t.Cleanup(func() { runCommand = old })
	Stop("dyna-version-probe-1.scope")
	if len(deadlines) != 1 || !deadlines[0] {
		t.Fatalf("kill context had a live deadline: %v; want one", deadlines)
	}
}
