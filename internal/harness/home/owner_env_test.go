package home

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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
	} // Shell jobs stand in for the harness's own shell, so they keep the
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

// A game helper sets fresh XDG_DATA_HOME and XDG_CONFIG_HOME, then runs a
// dyna client command (D-237). The caller keeps its roots, but the client must
// still reach the owner's data.
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
			roots := t.TempDir()
			freshData := filepath.Join(roots, "fresh-data")
			freshConfig := filepath.Join(roots, "fresh-config")
			setProcessEnv(t, merged(env, out))
			t.Setenv("XDG_DATA_HOME", freshData)
			t.Setenv("XDG_CONFIG_HOME", freshConfig)
			if err := RestoreOwner(tc.args); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("XDG_DATA_HOME") != freshData || os.Getenv("XDG_CONFIG_HOME") != freshConfig {
				t.Fatal("the caller's roots were replaced")
			}
			got, set := os.LookupEnv(store.DataDirEnv)
			if set != tc.want || tc.want && got != filepath.Join(owner, ".local", "share", "dyna") {
				t.Fatalf("%s = %q, %v; want the owner's folder: %v", store.DataDirEnv, got, set, tc.want)
			}
		})
	}
}

// A harness shell whose caller chose its own HOME or XDG_DATA_HOME never
// writes the owner's live bundle store (D-398). Bundle-writing commands refuse
// and name the two ways out; an explicit DYNA_DATA_DIR, other commands and
// read-only harness commands pass.
func TestRestoreOwnerRefusesBundleWritesWithCallerDataRoot(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string
		dataDir bool
		wantErr bool
	}{
		"install":                {[]string{"harness", "install", "pi"}, false, true},
		"login":                  {[]string{"harness", "login", "pi"}, false, true},
		"rm":                     {[]string{"harness", "rm", "pi", "--version", "1.0.0"}, false, true},
		"rollback":               {[]string{"harness", "rollback", "pi"}, false, true},
		"gc":                     {[]string{"harness", "gc"}, false, true},
		"explicit DYNA_DATA_DIR": {[]string{"harness", "install", "pi"}, true, false},
		"client command":         {[]string{"computer", "env"}, false, false},
		"read-only harness":      {[]string{"harness", "list"}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			scope, _, env := fixture(t)
			out, err := Prepare(scope, "", env)
			if err != nil {
				t.Fatal(err)
			}
			roots := t.TempDir()
			setProcessEnv(t, merged(env, out))
			t.Setenv("HOME", filepath.Join(roots, "home"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(roots, "data"))
			if tc.dataDir {
				t.Setenv(store.DataDirEnv, filepath.Join(roots, "store"))
			}
			err = RestoreOwner(tc.args)
			if tc.wantErr && (err == nil || !strings.Contains(err.Error(), "DYNA_DATA_DIR") || !strings.Contains(err.Error(), "scratch")) {
				t.Fatalf("RestoreOwner(%v) = %v; want the bundle-write refusal", tc.args, err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("RestoreOwner(%v) = %v; want no refusal", tc.args, err)
			}
		})
	}
}

// A caller-chosen HOME with an inherited XDG_DATA_HOME — the harness's
// private root or the owner's — must not steer `dyna engine run` into the
// owner's live store: the inherited root gives way and the data folder follows
// the caller's home (D-398).
func TestRestoreOwnerEngineFollowsCallerHome(t *testing.T) {
	for name, tc := range map[string]struct{ ownerXDG bool }{
		"private XDG_DATA_HOME inherited": {false},
		"owner XDG_DATA_HOME inherited":   {true},
	} {
		t.Run(name, func(t *testing.T) {
			scope, owner, env := fixture(t)
			if tc.ownerXDG {
				env = append(slices.Clone(env), "XDG_DATA_HOME="+filepath.Join(owner, ".local", "share"))
			}
			out, err := Prepare(scope, "", env)
			if err != nil {
				t.Fatal(err)
			}
			callerHome := filepath.Join(t.TempDir(), "caller-home")
			setProcessEnv(t, merged(env, out))
			t.Setenv("HOME", callerHome)
			if err := RestoreOwner([]string{"engine", "run"}); err != nil {
				t.Fatal(err)
			}
			if _, ok := os.LookupEnv(store.DataDirEnv); ok {
				t.Fatalf("%s set; want the data folder from the caller's home", store.DataDirEnv)
			}
			if got, want := store.DataDir(), filepath.Join(callerHome, ".local", "share", "dyna"); got != want {
				t.Fatalf("data folder %q; want %q", got, want)
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

// The engine may start with a PATH that lacks the owner's tool directories; the
// login shell's PATH adds them after the existing entries (D-265).
func TestLoginPathAddsOwnerToolDirectories(t *testing.T) {
	cargo := filepath.Join(t.TempDir(), ".cargo", "bin")
	shell := filepath.Join(t.TempDir(), "login-shell")
	script := "#!/bin/sh\necho banner\nprintf '%s' '/usr/bin:" + cargo + ":relative/bin' >\"$3\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	login := loginPathFrom(shell, nil)
	got := withLoginPath("/usr/local/bin:/usr/bin", login)
	if want := "/usr/local/bin:/usr/bin:" + cargo; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
	if loginPathFrom(filepath.Join(t.TempDir(), "missing"), nil) != "" {
		t.Fatal("a missing login shell gave a PATH")
	}
}

func TestPrepareGivesTheHarnessTheLoginPath(t *testing.T) {
	scope, _, env := fixture(t)
	cargo := filepath.Join(t.TempDir(), ".cargo", "bin")
	old := loginPathSource
	loginPathSource = func() string { return cargo }
	t.Cleanup(func() { loginPathSource = old })
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(filepath.SplitList(out["PATH"]), cargo) {
		t.Fatalf("harness PATH %q lacks the login PATH entry %q", out["PATH"], cargo)
	}
}

// A background process that shell startup leaves holding stdout must not stall
// the PATH lookup. The process blocks reading a FIFO that the test holds open,
// so no clock decides the result; closing the FIFO at cleanup ends the process
// by end-of-file.
func TestLoginPathDoesNotWaitForABackgroundProcess(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "hold")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// A read-write open never blocks, and the background reader finds a writer.
	hold, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hold.Close() })
	shell := filepath.Join(dir, "login-shell")
	// The shell opens the FIFO itself before it starts the reader, so the reader
	// exists with its end open when the shell returns, and ends when hold closes.
	script := "#!/bin/sh\nexec 7<\"$FIFO\"\ncat <&7 7<&- &\nprintf '%s' /opt/tools/bin >\"$3\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := loginPathFrom(shell, []string{"FIFO=" + fifo, "PATH=" + os.Getenv("PATH")}); got != "/opt/tools/bin" {
		t.Fatalf("PATH = %q", got)
	}
}
