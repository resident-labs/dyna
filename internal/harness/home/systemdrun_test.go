package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/shellquote"
)

// The systemd-run shim joins a room's own user units to the room slice: it
// adds --slice only for --user calls that chose no slice of their own, never
// touches other calls, and passes every argument through unchanged.
func TestSystemdRunShimJoinsTheRoomSlice(t *testing.T) {
	scope, _, env := fixture(t)
	bin := envMap(env)["PATH"]
	write(t, filepath.Join(bin, "systemd-run"), "#!/bin/sh\nprintf '%s\\n' \"$@\"\n", 0700)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(filepath.Dir(out["DYNA_BIN"]), "systemd-run")
	slice := `dyna-room-room\x2dlimits.slice`
	inRoom := merged(append(env, "DYNA_ROOM_SLICE="+slice), out)
	plain := merged(env, out)

	for _, tc := range []struct {
		name string
		env  []string
		args []string
		want string
	}{
		{"a user call joins the room slice", inRoom, []string{"--user", "--", "/bin/true", "prog"},
			"--slice=" + slice + "\n--user\n--\n/bin/true\nprog\n"},
		{"a degraded room still joins the shared slice", merged(append(env, "DYNA_ROOM_SLICE="+`dyna.slice`), out), []string{"--user", "/bin/true"},
			"--slice=dyna.slice\n--user\n/bin/true\n"},
		{"a unit name with a separate value still scans", inRoom, []string{"-u", "foo.service", "--user", "/bin/true"},
			"--slice=" + slice + "\n-u\nfoo.service\n--user\n/bin/true\n"},
		{"a description with a separate value still scans", inRoom, []string{"--description", "x y", "--user", "/bin/true"},
			"--slice=" + slice + "\n--description\nx y\n--user\n/bin/true\n"},
		{"an attached unit value still scans", inRoom, []string{"--unit=foo.service", "--user", "/bin/true"},
			"--slice=" + slice + "\n--unit=foo.service\n--user\n/bin/true\n"},
		{"an environment with a separate value still scans", inRoom, []string{"-E", "A=b", "--user", "/bin/true"},
			"--slice=" + slice + "\n-E\nA=b\n--user\n/bin/true\n"},
		{"a timer delay with a separate value still scans", inRoom, []string{"--on-active", "30", "--user", "/bin/true"},
			"--slice=" + slice + "\n--on-active\n30\n--user\n/bin/true\n"},
		{"an attached non-slice property still joins", inRoom, []string{"-pMemoryMax=1G", "--user", "/bin/true"},
			"--slice=" + slice + "\n-pMemoryMax=1G\n--user\n/bin/true\n"},
		{"a slice the caller inherits stays", inRoom, []string{"--slice-inherit", "--user", "/bin/true"},
			"--slice-inherit\n--user\n/bin/true\n"},
		{"a slice the caller chose stays", inRoom, []string{"--user", "--slice=mine.scope.slice", "--", "/bin/true"},
			"--user\n--slice=mine.scope.slice\n--\n/bin/true\n"},
		{"a property slice stays", inRoom, []string{"--user", "-p", "Slice=other.slice", "/bin/true"},
			"--user\n-p\nSlice=other.slice\n/bin/true\n"},
		{"a scope call joins the room slice", inRoom, []string{"--user", "--scope", "/bin/true"},
			"--slice=" + slice + "\n--user\n--scope\n/bin/true\n"},
		{"a system call stays", inRoom, []string{"--unit=supervisor", "/bin/true"},
			"--unit=supervisor\n/bin/true\n"},
		{"no room slice stays", plain, []string{"--user", "/bin/true"},
			"--user\n/bin/true\n"},
		{"arguments keep their shape", inRoom, []string{"--user", "/bin/echo", "space argument", "$HOME", "'quoted'", ""},
			"--slice=" + slice + "\n--user\n/bin/echo\nspace argument\n$HOME\n'quoted'\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.env, shim, tc.args...); got != tc.want {
				t.Fatalf("shim output = %q, want %q", got, tc.want)
			}
		})
	}

	// The shim execs the owner's real systemd-run, never itself.
	script, err := os.ReadFile(shim)
	if err != nil {
		t.Fatal(err)
	}
	if want := "exec " + shellquote.Quote(filepath.Join(bin, "systemd-run")); !strings.Contains(string(script), want+" \"$@\"") {
		t.Fatalf("shim does not exec the resolved real binary: %s", script)
	}
}

// A machine without systemd-run still prepares; no shim is published.
func TestSystemdRunShimSkippedWithoutTheBinary(t *testing.T) {
	scope, _, env := fixture(t)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(out["DYNA_BIN"]), "systemd-run")); !os.IsNotExist(err) {
		t.Fatalf("systemd-run published without a real binary: %v", err)
	}
}
