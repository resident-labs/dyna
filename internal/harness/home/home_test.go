package home

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/fsutil"
)

func write(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	writeFile := os.WriteFile
	if mode&0111 != 0 {
		writeFile = fsutil.WriteExecutable
	}
	if err := writeFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) (string, string, []string) {
	t.Helper()
	base := t.TempDir()
	owner := filepath.Join(base, "owner ' dollar$ ` tick")
	bin := filepath.Join(base, "tools ' bin")
	for _, name := range []string{"dyna", "git"} {
		write(t, filepath.Join(bin, name), "#!/bin/sh\nprintf '%s\\n' \"$HOME\" \"${XDG_CONFIG_HOME-unset}\" \"${XDG_DATA_HOME-unset}\" \"${XDG_CACHE_HOME-unset}\" \"${XDG_STATE_HOME-unset}\" \"$@\"\n", 0700)
	}
	return filepath.Join(base, "native"), owner, []string{"HOME=" + owner, "PATH=" + bin, "XDG_CONFIG_HOME=", "XDG_CACHE_HOME=" + filepath.Join(owner, "cache ' $"), "ENV=bad", "BASH_ENV=bad", "ZDOTDIR=bad"}
}
func merged(env []string, overrides map[string]string) []string {
	values := envMap(env)
	for key, value := range overrides {
		values[key] = value
	}
	result := []string{}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
func run(t *testing.T, env []string, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, output)
	}
	return string(output)
}

func TestRootsAndBoundary(t *testing.T) {
	scope, owner, env := fixture(t)
	sentinel := filepath.Join(owner, ".config/personal/mcp-and-skills")
	write(t, sentinel, "personal canary", 0600)
	// A non-directory native scope proves Ensure never enters that tree.
	write(t, scope, "native credential", 0600)
	if Home(scope) != scope+".home" || !reflect.DeepEqual(Roots(scope), Roots(scope)) {
		t.Fatal("unstable roots")
	}
	if err := Ensure(scope); err != nil {
		t.Fatal(err)
	}
	cache := Roots(scope)["XDG_CACHE_HOME"]
	socket, err := net.Listen("unix", filepath.Join(cache, "tool.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err := os.Symlink(owner, filepath.Join(cache, "tool-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Home(scope), 0755); err != nil {
		t.Fatal(err)
	}
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	for key, path := range Roots(scope) {
		if out[key] != path {
			t.Fatal(key)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("%s mode: %v %v", key, info, err)
		}
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "personal canary" {
		t.Fatalf("owner changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out["HOME"], ".config/personal/mcp-and-skills")); !os.IsNotExist(err) {
		t.Fatal("personal discovery copied")
	}
	if err := Ensure(scope); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ENV", "BASH_ENV", "ZDOTDIR"} {
		if out[key] != "" {
			t.Fatal(key)
		}
	}
}

func TestRejectSymlinkRoots(t *testing.T) {
	for _, kind := range []string{"home", "ancestor", "xdg", "bridge", "config"} {
		t.Run(kind, func(t *testing.T) {
			scope, _, env := fixture(t)
			target := t.TempDir()
			switch kind {
			case "home":
				if err := os.Symlink(target, Home(scope)); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				link := filepath.Join(filepath.Dir(scope), "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				scope = filepath.Join(link, "native")
			case "xdg":
				if err := Ensure(scope); err != nil {
					t.Fatal(err)
				}
				path := Roots(scope)["XDG_CONFIG_HOME"]
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "bridge", "config":
				out, err := Prepare(scope, "", env)
				if err != nil {
					t.Fatal(err)
				}
				dest := out["GIT_CONFIG_GLOBAL"]
				if kind == "bridge" {
					dest = out["DYNA_BIN"]
				}
				write(t, filepath.Join(target, "unchanged"), "safe", 0600)
				if err := os.Remove(dest); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(target, "unchanged"), dest); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Prepare(scope, "", env); err == nil {
				t.Fatal("accepted symlink")
			}
		})
	}
}

func TestBridgeRestorationAndArguments(t *testing.T) {
	scope, owner, env := fixture(t)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"space argument", "'quoted'", "$(touch should-not-exist)", "line\nbreak", "", "*"}
	restored := strings.Join(append([]string{owner, "", "unset", filepath.Join(owner, "cache ' $"), "unset"}, args...), "\n") + "\n"
	// The dyna bridge only forwards; dyna restores the owner roots itself.
	private := strings.Join(append([]string{out["HOME"], out["XDG_CONFIG_HOME"], out["XDG_DATA_HOME"], out["XDG_CACHE_HOME"], out["XDG_STATE_HOME"]}, args...), "\n") + "\n"
	for name, expected := range map[string]string{"dyna": private, "git": restored} {
		path := filepath.Join(filepath.Dir(out["DYNA_BIN"]), name)
		if got := run(t, merged(env, out), path, args...); got != expected {
			t.Fatalf("bridge %s: got %q want %q", name, got, expected)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("script mode: %v", err)
		}
	}
	info, err := os.Stat(out["GIT_CONFIG_GLOBAL"])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("config mode")
	}
	// Both bridges use the resolved absolute target, even with their bin first.
	script, err := os.ReadFile(out["DYNA_BIN"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "exec '"+strings.ReplaceAll(filepath.Join(filepath.Dir(scope), "tools ' bin/dyna"), "'", `'\''`)+"'") {
		t.Fatal("missing absolute target")
	}
}

func TestBridgeUnsetHomeAndPath(t *testing.T) {
	scope, _, env := fixture(t)
	real := filepath.Join(envMap(env)["PATH"], "dyna")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	// HOME remains absent in the bridge, even though private tool defaults need an owner home.
	out, err := Prepare(scope, real, []string{"PATH=" + filepath.Dir(git)})
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(filepath.Dir(out["DYNA_BIN"]), "git"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "unset HOME\n") {
		t.Fatal("HOME presence lost")
	}
	scriptText := bridge(map[string]string{}, real, false)
	if !strings.Contains(scriptText, "unset PATH\n") {
		t.Fatal("PATH presence lost")
	}
}

func TestToolOptInAndExplicitValues(t *testing.T) {
	for _, value := range []string{"../shell", "/bin/sh", "sh;evil", "good,,bad", "-bad", "..", "has space"} {
		t.Run(value, func(t *testing.T) {
			scope, _, env := fixture(t)
			if _, err := Prepare(scope, "", append(env, "DYNA_HOME_TOOLS="+value)); err == nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("bad name accepted: %v", err)
			}
		})
	}
	scope, owner, env := fixture(t)
	if _, err := Prepare(scope, "", append(env, "DYNA_HOME_TOOLS=missing")); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatal("missing opt-in tool accepted")
	}
	write(t, filepath.Join(envMap(env)["PATH"], "cloud-cli"), "#!/bin/sh\nprintf '%s' \"$HOME\"\n", 0700)
	env = append(env, "DYNA_HOME_TOOLS=cloud-cli,git,cloud-cli", "MISE_DATA_DIR=", "CARGO_HOME=/explicit/cargo", "GH_CONFIG_DIR=", "GOPATH=/owner/go")
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"MISE_DATA_DIR": "", "CARGO_HOME": "/explicit/cargo", "GH_CONFIG_DIR": "", "GOMODCACHE": "/owner/go/pkg/mod"} {
		if out[key] != want {
			t.Fatalf("%s: %q", key, out[key])
		}
	}
	if got := run(t, merged(env, out), filepath.Join(filepath.Dir(out["DYNA_BIN"]), "cloud-cli")); got != owner {
		t.Fatal(got)
	}
}

func TestDiscoveryStaysPrivateAndPreparePreservesNativeValues(t *testing.T) {
	scope, _, env := fixture(t)
	discovery := Discovery(scope)
	for key, path := range discovery {
		if path == "" {
			continue
		}
		relative, err := filepath.Rel(Home(scope), path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("%s escapes private HOME: %q", key, path)
		}
	}
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "ANTHROPIC_CONFIG_DIR", "GROK_HOME", "PI_CODING_AGENT_DIR"} {
		if discovery[key] == "" {
			t.Fatal("missing discovery root: " + key)
		}
	}
	if len(Roots(scope)) != 5 {
		t.Fatal("Roots must stay limited to HOME/XDG")
	}
	native := map[string]string{"PI_CODING_AGENT_DIR": scope}
	env = merged(merged(env, discovery), native)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	result := envMap(merged(env, out))
	for key, value := range native {
		if result[key] != value {
			t.Fatalf("Prepare replaced native %s", key)
		}
	}
}

func TestBridgeRetentionAndNestedPrepare(t *testing.T) {
	scope, owner, env := fixture(t)
	bin := envMap(env)["PATH"]
	write(t, filepath.Join(bin, "cloud"), "#!/bin/sh\nexit 0\n", 0700)
	out, err := Prepare(scope, "", append(env, "DYNA_HOME_TOOLS=cloud"))
	if err != nil {
		t.Fatal(err)
	}
	earlierCloud := filepath.Join(filepath.Dir(out["DYNA_BIN"]), "cloud")
	out, err = Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(earlierCloud); err != nil {
		t.Fatal("active earlier bridge was removed")
	}
	setProcessEnv(t, merged(env, out))
	if err := RestoreOwner(nil); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HOME") != owner {
		t.Fatal("nested owner HOME not restored")
	}
	nested, err := Prepare(scope, os.Getenv("DYNA_BIN"), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(nested["GIT_CONFIG_GLOBAL"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), gitQuote(nested["GIT_CONFIG_GLOBAL"])) {
		t.Fatal("nested config includes itself")
	}
}

func TestSymlinkShimKeepsToolIdentity(t *testing.T) {
	scope, _, env := fixture(t)
	bin := envMap(env)["PATH"]
	tool := filepath.Join(filepath.Dir(scope), "multicall")
	write(t, tool, "#!/bin/sh\ncase \"${0##*/}\" in\ngo) printf '%s' '{\"GOPATH\":\"/shim/go\",\"GOMODCACHE\":\"/shim/mod\",\"GOCACHE\":\"/shim/cache\"}' ;;\ncloud-cli) printf '%s' cloud-cli ;;\n*) exit 19 ;;\nesac\n", 0700)
	for _, name := range []string{"go", "cloud-cli"} {
		if err := os.Symlink(tool, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := Prepare(scope, "", append(env, "DYNA_HOME_TOOLS=cloud-cli"))
	if err != nil {
		t.Fatal(err)
	}
	if out["GOPATH"] != "/shim/go" {
		t.Fatalf("go shim identity lost: %q", out["GOPATH"])
	}
	if got := run(t, merged(env, out), filepath.Join(filepath.Dir(out["DYNA_BIN"]), "cloud-cli")); got != "cloud-cli" {
		t.Fatal(got)
	}
	if err := Ensure("relative/native"); err == nil {
		t.Fatal("relative Ensure accepted")
	}
}

func TestConcurrentPrepare(t *testing.T) {
	scope, _, env := fixture(t)
	failures := make(chan error, 12)
	for range 12 {
		go func() { _, err := Prepare(scope, "", env); failures <- err }()
	}
	for range 12 {
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
	}
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	run(t, merged(env, out), out["DYNA_BIN"], "ready")
	entries, err := os.ReadDir(filepath.Dir(out["DYNA_BIN"]))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected bridge entries: %v", entries)
	}
}

func TestDynaMCPServeKeepsPrivateRoots(t *testing.T) {
	scope, owner, env := fixture(t)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	setProcessEnv(t, merged(env, out))
	if err := RestoreOwner([]string{"mcp", "serve", "/absolute/generation", "server"}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HOME") != out["HOME"] || os.Getenv("XDG_DATA_HOME") != out["XDG_DATA_HOME"] || os.Getenv(OwnerEnvKey) == "" {
		t.Fatal("MCP discovery roots changed")
	}
	for _, args := range [][]string{{"room", "log"}, {"journal"}, {"mcp", "list"}} {
		setProcessEnv(t, merged(env, out))
		if err := RestoreOwner(args); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("HOME") != owner {
			t.Fatalf("%v did not restore roots", args)
		}
	}
}

func TestPrepareRemovesLegacyWorktreesLink(t *testing.T) {
	scope, owner, env := fixture(t)
	if err := Ensure(scope); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(Home(scope), ".dyna-worktrees")
	if err := os.Symlink(filepath.Join(owner, ".dyna-worktrees"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(scope, "", env); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("legacy link still present: %v", err)
	}
}

func TestConcurrentLegacyLinkRemoval(t *testing.T) {
	scope, owner, _ := fixture(t)
	if err := Ensure(scope); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(Home(scope), ".dyna-worktrees")
	if err := os.Symlink(filepath.Join(owner, ".dyna-worktrees"), link); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 16)
	for i := 0; i < cap(errs); i++ {
		go func() { errs <- removeWorktreesLink(scope) }()
	}
	for i := 0; i < cap(errs); i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrepareCreatesNoWorktreesLink(t *testing.T) {
	scope, _, env := fixture(t)
	if _, err := Prepare(scope, "", env); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(Home(scope), ".dyna-worktrees")); !os.IsNotExist(err) {
		t.Fatalf("private HOME has .dyna-worktrees: %v", err)
	}
}

func TestExistingPrivateWorktreesDirectoryIsKept(t *testing.T) {
	scope, _, env := fixture(t)
	write(t, filepath.Join(Home(scope), ".dyna-worktrees", "old", "file"), "x", 0600)
	if _, err := Prepare(scope, "", env); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(Home(scope), ".dyna-worktrees", "old", "file")); err != nil {
		t.Fatal("existing private worktrees were disturbed")
	}
}

// The dyna bridge execs the dyna the owner's PATH installs, not the build
// this engine runs. An update replaces the installed binary in place, so a
// session that keeps running across the update still starts every later
// command, and its shell MCP server, on the current build with no engine
// help. A bridge that named the engine's image copy kept the session on the
// build its first turn chose until a schema bump cut its shells off (D-171).
func TestDynaBridgeExecsTheInstalledDyna(t *testing.T) {
	scope, owner, env := fixture(t)
	image := filepath.Join(owner, "engine-image")
	write(t, image, "#!/bin/sh\nprintf 'engine image'\n", 0700)
	out, err := Prepare(scope, image, env)
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(out["DYNA_BIN"])
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(envMap(env)["PATH"], "dyna")
	if !strings.Contains(string(script), "exec '"+strings.ReplaceAll(installed, "'", `'\''`)+"'") {
		t.Fatalf("dyna bridge does not exec the installed dyna %q: %s", installed, script)
	}
	if strings.Contains(string(script), "engine-image") {
		t.Fatalf("dyna bridge pins the engine image: %s", script)
	}
	// The engine's own path still wins when the owner PATH names no dyna, so
	// an engine without an installation keeps working.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	out, err = Prepare(scope, image, []string{"HOME=" + owner, "PATH=" + filepath.Dir(git)})
	if err != nil {
		t.Fatal(err)
	}
	script, err = os.ReadFile(out["DYNA_BIN"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "exec '"+strings.ReplaceAll(image, "'", `'\''`)+"'") {
		t.Fatalf("dyna bridge lost the engine path fallback: %s", script)
	}
}
