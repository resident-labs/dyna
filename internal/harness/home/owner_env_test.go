package home

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/store"
)

// setProcessEnv replaces the process environment with env until the test ends.
func setProcessEnv(t *testing.T, env []string) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	for key, value := range envMap(env) {
		t.Setenv(key, value)
	}
}

func TestRestoreOwnerUndoesPrivateRoots(t *testing.T) {
	scope, owner, env := fixture(t)
	bin := envMap(env)["PATH"]
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	launch := merged(env, out)
	extra := filepath.Join(filepath.Dir(scope), "harness bin")
	launch = append(launch, "PATH="+extra+string(os.PathListSeparator)+out["PATH"])
	setProcessEnv(t, launch)
	if err := RestoreOwner([]string{"room", "list"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"HOME": owner, "XDG_CONFIG_HOME": "", "XDG_CACHE_HOME": filepath.Join(owner, "cache ' $"), "DYNA_BIN": filepath.Join(bin, "dyna"), "PATH": extra + string(os.PathListSeparator) + bin}
	for key, value := range want {
		if got, ok := os.LookupEnv(key); !ok || got != value {
			t.Errorf("%s = %q, %v; want %q", key, got, ok, value)
		}
	}
	for _, key := range []string{"XDG_DATA_HOME", "XDG_STATE_HOME", "GIT_CONFIG_GLOBAL", OwnerEnvKey} {
		if value, ok := os.LookupEnv(key); ok {
			t.Errorf("%s = %q; want unset", key, value)
		}
	}	// Shell jobs stand in for the harness's own shell, so they keep the
	// environment the process started with.
	if got, want := envMap(LaunchEnv()), envMap(launch); got["HOME"] != want["HOME"] || got["PATH"] != want["PATH"] || got[OwnerEnvKey] == "" {
		t.Errorf("LaunchEnv HOME=%q PATH=%q owner=%v; want the private launch", got["HOME"], got["PATH"], got[OwnerEnvKey] != "")
	}
}

func TestRestoreOwnerKeepsCallerRoots(t *testing.T) {
	scope, _, env := fixture(t)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	// A caller that chose its own data root, such as a test, keeps it.
	custom := filepath.Join(t.TempDir(), "data")
	setProcessEnv(t, merged(env, out))
	t.Setenv("XDG_DATA_HOME", custom)
	if err := RestoreOwner(nil); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HOME") != out["HOME"] || os.Getenv("XDG_DATA_HOME") != custom {
		t.Fatal("caller roots were replaced")
	}
	if _, ok := os.LookupEnv(OwnerEnvKey); ok {
		t.Fatal("owner record leaked to children")
	}
	t.Setenv(OwnerEnvKey, "{not json")
	if err := RestoreOwner(nil); err == nil {
		t.Fatal("invalid owner record accepted")
	}
}

// A game helper sets fresh XDG roots, then runs a dyna client command. The
// caller keeps its roots, but the client must still reach the owner's data.
func TestRestoreOwnerKeepsOwnerDataForClientsWithCallerRoots(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want bool
	}{
		"client command": {[]string{"computer", "env"}, true},
		"engine":         {[]string{"engine", "run"}, false},
		"no command":     {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			scope, owner, env := fixture(t)
			out, err := Prepare(scope, "", env)
			if err != nil {
				t.Fatal(err)
			}
			fresh := filepath.Join(t.TempDir(), "fresh-data")
			setProcessEnv(t, merged(env, out))
			t.Setenv("XDG_DATA_HOME", fresh)
			if err := RestoreOwner(tc.args); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("XDG_DATA_HOME") != fresh {
				t.Fatal("the caller's data root was replaced")
			}
			got, set := os.LookupEnv(store.DataDirEnv)
			if set != tc.want || tc.want && got != filepath.Join(owner, ".local", "share", "dyna") {
				t.Fatalf("%s = %q, %v; want the owner's folder: %v", store.DataDirEnv, got, set, tc.want)
			}
		})
	}
}

// Git and opt-in tool bridges restore HOME and XDG but keep the private
// DYNA_BIN and Git config. A dyna they start must finish the restore.
func TestRestoreOwnerAfterToolBridge(t *testing.T) {
	scope, owner, env := fixture(t)
	bin := envMap(env)["PATH"]
	envTool, err := exec.LookPath("env")
	if err != nil {
		t.Skip("env unavailable")
	}
	write(t, filepath.Join(bin, "envdump"), "#!/bin/sh\nexec "+shellquote.Quote(envTool)+" -0\n", 0700)
	out, err := Prepare(scope, "", append(env, "DYNA_HOME_TOOLS=envdump"))
	if err != nil {
		t.Fatal(err)
	}
	dump := run(t, merged(env, out), filepath.Join(filepath.Dir(out["DYNA_BIN"]), "envdump"))
	setProcessEnv(t, strings.Split(strings.TrimSuffix(dump, "\x00"), "\x00"))
	if os.Getenv("HOME") != owner || os.Getenv("DYNA_BIN") != out["DYNA_BIN"] {
		t.Fatal("fixture does not model a tool bridge")
	}
	if err := RestoreOwner([]string{"account", "login"}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DYNA_BIN") != filepath.Join(bin, "dyna") {
		t.Fatalf("DYNA_BIN = %q; want the real dyna", os.Getenv("DYNA_BIN"))
	}
	if value, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		t.Fatalf("GIT_CONFIG_GLOBAL = %q; want unset", value)
	}
	if _, err := Prepare(scope, os.Getenv("DYNA_BIN"), os.Environ()); err != nil {
		t.Fatalf("nested Prepare: %v", err)
	}
}
