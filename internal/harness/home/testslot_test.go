package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestSlotWrapperKeepsOwnerToolBridge(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	write(t, filepath.Join(config, "dyna", "settings.json"), `{"testSlots":{"commands":[{"command":"alpha","arg":"test"}]}}`, 0600)
	scope, owner, env := fixture(t)
	bin := envMap(env)["PATH"]
	write(t, filepath.Join(bin, "alpha"), "#!/bin/sh\nprintf '%s\\n' \"$HOME\" \"$XDG_CACHE_HOME\" \"$PATH\" \"$@\"\n", 0700)
	out, err := Prepare(scope, "", append(env, "DYNA_HOME_TOOLS=alpha"))
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(filepath.Dir(out["DYNA_BIN"]), "alpha")
	for _, args := range [][]string{{"version"}, {"test"}} {
		got := run(t, merged(env, out), wrapper, args...)
		if !strings.HasPrefix(got, owner+"\n") || !strings.Contains(got, filepath.Join(owner, "cache ' $")) {
			t.Fatalf("%v did not restore the owner roots: %q", args, got)
		}
		if args[0] == "test" && !strings.Contains(got, "\nslot\nrun\n--\n") {
			t.Fatalf("test did not use a slot: %q", got)
		}
	}
}

func TestTestSlotWrappersFollowSettings(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	write(t, filepath.Join(config, "dyna", "settings.json"), `{"testSlots":{"slots":2,"commands":[{"command":"alpha","arg":"test"},{"command":"missing-test-slot-tool","arg":""}]}}`, 0600)
	scope, _, env := fixture(t)
	bin := envMap(env)["PATH"]
	write(t, filepath.Join(bin, "alpha"), "#!/bin/sh\nprintf 'direct:%s\\n' \"$*\"\n", 0700)
	write(t, filepath.Join(bin, "beta"), "#!/bin/sh\nexit 0\n", 0700)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	published := filepath.Dir(out["DYNA_BIN"])
	if _, err := os.Stat(filepath.Join(published, "alpha")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"beta", "missing-test-slot-tool"} {
		if _, err := os.Stat(filepath.Join(published, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected wrapper %s: %v", name, err)
		}
	}
	wrapper := filepath.Join(published, "alpha")
	if got := run(t, merged(env, out), wrapper, "--version"); got != "direct:--version\n" {
		t.Fatalf("non-test got %q", got)
	}
	if got := run(t, merged(env, out), wrapper, "script", "test"); got != "direct:script test\n" {
		t.Fatalf("positional argument got %q", got)
	}
	if got := run(t, merged(env, out), wrapper, "--flag", "test", "./..."); !strings.Contains(got, "\nslot\nrun\n--\n") {
		t.Fatalf("test did not invoke slot: %q", got)
	}
}
