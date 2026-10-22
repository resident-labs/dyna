package agentslice

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunForeground(t *testing.T) {
	config, calls := fakeManager(t)
	t.Setenv("DYNA_FOREGROUND_SCOPE", "")
	t.Setenv("DYNA_SLICE", "off") // Disabling agent scopes must not lower the UI.
	argsPath := filepath.Join(t.TempDir(), "args")
	bin := filepath.Join(t.TempDir(), "systemd-run")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$SCOPE_ARGS\"\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	lookPath = func(string) (string, error) { return bin, nil }
	cmd := exec.Command("/bin/sh", "-c", "printf '%s\\n' \"$DYNA_FOREGROUND_SCOPE\" \"$VALUE\" \"$1\"", "desktop", "two words")
	cmd.Env = append(os.Environ(), "SCOPE_ARGS="+argsPath, "VALUE=kept")
	var out bytes.Buffer
	cmd.Stdout = &out
	handled, err := RunForeground(cmd, "desktop")
	if !handled || err != nil {
		t.Fatalf("foreground command handled=%v, err=%v", handled, err)
	}
	if got := out.String(); got != "desktop\nkept\ntwo words\n" {
		t.Fatalf("child environment or arguments changed: %q", got)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--slice=app.slice\n", "--property=CPUWeight=200\n", "--property=IOWeight=200\n", "--unit=dyna-desktop-"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("scope arguments missing %q: %s", want, args)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("desktop changed user units: %v", *calls)
	}
	if _, err := os.Stat(filepath.Join(config, "systemd")); !os.IsNotExist(err) {
		t.Fatalf("desktop wrote slice policy: %v", err)
	}
}

func TestRunForegroundFallbackAndGuard(t *testing.T) {
	fakeManager(t)
	for _, marker := range []string{"desktop", ""} {
		t.Setenv("DYNA_FOREGROUND_SCOPE", marker)
		if marker == "" {
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
		}
		cmd := exec.Command("/must-not-run")
		handled, err := RunForeground(cmd, "desktop")
		if handled || err != nil || cmd.Path != "/must-not-run" {
			t.Fatalf("marker=%q handled=%v err=%v cmd=%q", marker, handled, err, cmd.Path)
		}
	}
}

func TestRunForegroundReturnsChildError(t *testing.T) {
	fakeManager(t)
	t.Setenv("DYNA_FOREGROUND_SCOPE", "")
	lookPath = func(string) (string, error) { return "/bin/false", nil }
	handled, err := RunForeground(exec.Command("/must-not-run"), "desktop")
	if !handled || err == nil {
		t.Fatalf("failed scope handled=%v err=%v", handled, err)
	}
}
