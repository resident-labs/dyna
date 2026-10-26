//go:build linux

package computers

import (
	"context"
	"dyna-agent/internal/testsafe"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingComputerUnitNamesInstaller(t *testing.T) {
	for _, loadState := range []string{"not-found", "loaded"} {
		t.Run(loadState, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/temporary/fake-session-bus")
			t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path=/temporary/fake-system-bus")
			bin := t.TempDir()
			t.Setenv("PATH", bin)
			t.Setenv("FAKE_UNIT_LOAD_STATE", loadState)
			log := filepath.Join(bin, "commands")
			t.Setenv("SYSTEMCTL_LOG", log)
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$SYSTEMCTL_LOG"
case "$*" in
  '--user start dyna-computers.service') printf 'computer start failed\n' >&2; exit 5 ;;
  '--user show dyna-computers.service --property=LoadState --value') printf '%s\n' "$FAKE_UNIT_LOAD_STATE" ;;
  *) exit 99 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			err := startComputerService(context.Background())
			if err == nil {
				t.Fatal("failed service start succeeded")
			}
			if got, want := strings.Contains(err.Error(), "dyna engine install-services"), loadState == "not-found"; got != want {
				t.Fatalf("installer hint=%v want=%v: %v", got, want, err)
			}
			commands, readErr := os.ReadFile(log)
			if readErr != nil || !strings.Contains(string(commands), "--user start dyna-computers.service") || !strings.Contains(string(commands), "--property=LoadState") {
				t.Fatalf("fake systemctl commands: %s, %v", commands, readErr)
			}
		})
	}
}
