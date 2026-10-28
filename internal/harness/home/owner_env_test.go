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

// A harness shell that picks its own data roots never writes the owner's
// live store (D-398). A QA command with scratch HOME and XDG_DATA_HOME keeps
// them, DYNA_DATA_DIR stays unset, and the resolved data folder follows the
// caller's roots, for `dyna engine run` as for any command: a chosen
// XDG_DATA_HOME rules, and a chosen HOME drops an inherited XDG_DATA_HOME,
// whether the harness's private root or the owner's.
func TestRestoreOwnerKeepsCallerDataRoot(t *testing.T) {
	type xdgChoice int
	const (
		xdgLeft xdgChoice = iota
		xdgCaller
		xdgOwner
	)
	for name, tc := range map[string]struct {
		home   bool
		xdg    xdgChoice
		engine bool
	}{
		"HOME and XDG_DATA_HOME":                     {home: true, xdg: xdgCaller},
		"HOME only":                                  {home: true, xdg: xdgLeft},
		"HOME with the owner's XDG_DATA_HOME":        {home: true, xdg: xdgOwner},
		"XDG_DATA_HOME only":                         {xdg: xdgCaller},
		"engine run, HOME only":                      {home: true, xdg: xdgLeft, engine: true},
		"engine run, HOME with the owner's XDG data": {home: true, xdg: xdgOwner, engine: true},
	} {
		t.Run(name, func(t *testing.T) {
			scope, owner, env := fixture(t)
			// The owner has a data root of its own, so an inherited owner
			// value is distinct from both the private root and a caller's.
			ownerData := filepath.Join(owner, ".local", "share")
			env = append(slices.Clone(env), "XDG_DATA_HOME="+ownerData)
			out, err := Prepare(scope, "", env)
			if err != nil {
				t.Fatal(err)
			}
			roots := t.TempDir()
			home := filepath.Join(roots, "home")
			fresh := filepath.Join(roots, "data")
			setProcessEnv(t, merged(env, out))
			if tc.home {
				t.Setenv("HOME", home)
			}
			switch tc.xdg {
			case xdgCaller:
				t.Setenv("XDG_DATA_HOME", fresh)
			case xdgOwner:
				t.Setenv("XDG_DATA_HOME", ownerData)
			}
			args := []string{"harness", "install", "pi"}
			if tc.engine {
				args = []string{"engine", "run"}
			}
			if err := RestoreOwner(args); err != nil {
				t.Fatal(err)
			}
			if value, ok := os.LookupEnv(store.DataDirEnv); ok {
				t.Fatalf("%s = %q; want unset so the caller's data root rules", store.DataDirEnv, value)
			}
			want := filepath.Join(home, ".local", "share", "dyna")
			if tc.xdg == xdgCaller {
				want = filepath.Join(fresh, "dyna")
			}
			if got := store.DataDir(); got != want {
				t.Fatalf("data folder %q; want %q from the caller's roots", got, want)
			}
		})
	}
}

// A game helper sets fresh XDG roots, then runs a dyna client command. The
// caller keeps its roots, but the client must still reach the owner's data.
// The caller leaves every data root (HOME, XDG_DATA_HOME, DYNA_DATA_DIR)
// alone; a root it chose itself instead rules the data folder (D-398).
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
			fresh := filepath.Join(t.TempDir(), "fresh-config")
			setProcessEnv(t, merged(env, out))
			t.Setenv("XDG_CONFIG_HOME", fresh)
			if err := RestoreOwner(tc.args); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("XDG_CONFIG_HOME") != fresh {
				t.Fatal("the caller's config root was replaced")
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
