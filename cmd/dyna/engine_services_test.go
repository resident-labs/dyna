package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageServiceWriteOnlyAndPortDropIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/temporary/fake-bus")
	dir := t.TempDir()
	cmd := newEngineCommand()
	cmd.SetArgs([]string{"install-services", "--write-only", "--unit-dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dyna-engine.service", "dyna-computers.service"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "default.target.wants")); !os.IsNotExist(err) {
		t.Fatal("image service generation enabled units")
	}
	dropIn, err := os.ReadFile("../../image/overlay/etc/systemd/user/dyna-engine.service.d/ports.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Environment=DYNA_SERVE_PORT=7467", "Environment=DYNA_CARD_PORT=7468", "EnvironmentFile=-/etc/dyna/serve.env", "ExecStart=\nExecStart=/usr/local/bin/dyna engine run --addr 127.0.0.1:${DYNA_SERVE_PORT} --card-addr 127.0.0.1:${DYNA_CARD_PORT}"} {
		if !strings.Contains(string(dropIn), want) {
			t.Fatalf("image lost port configuration %q: %s", want, dropIn)
		}
	}
	finalize, err := os.ReadFile("../../image/finalize.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(finalize), "/usr/local/bin/dyna engine install-services --write-only --unit-dir /etc/systemd/user") {
		t.Fatal("image does not use the shared unit writer")
	}
}
