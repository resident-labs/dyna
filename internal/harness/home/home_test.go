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
	// No test spawns the owner's real login shell.
	old := loginPathSource
	loginPathSource = func() string { return "" }
	t.Cleanup(func() { loginPathSource = old })
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
				dest := publishedConfig(out)
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
	info, err := os.Stat(publishedConfig(out))
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
	config, err := os.ReadFile(publishedConfig(nested))
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
	// The directory holds only a git stand-in: a real tool directory would make
	// Prepare run the owner's go to read its roots, which takes seconds.
	bare := filepath.Join(filepath.Dir(owner), "bare bin")
	write(t, filepath.Join(bare, "git"), "#!/bin/sh\ntrue\n", 0700)
	out, err = Prepare(scope, image, []string{"HOME=" + owner, "PATH=" + bare})
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

// publishedConfig is the immutable Git config of a publication. GIT_CONFIG_GLOBAL
// names a writable file that includes it.
func publishedConfig(out map[string]string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(out["DYNA_BIN"])), "gitconfig")
}

// D-288: a tool that writes the global Git config (gh auth setup-git, git config
// --global) must not damage the published set, and its write must survive the
// next Prepare.
func TestGlobalGitWriteLeavesPublicationValid(t *testing.T) {
	scope, _, env := fixture(t)
	first, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	write := exec.Command(git, "config", "--global", "user.name", "Tool Wrote This")
	write.Env = merged(env, first)
	if out, err := write.CombinedOutput(); err != nil {
		t.Fatalf("git config --global: %s %v", out, err)
	}
	second, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatalf("Prepare after a global Git write: %v", err)
	}
	if second["DYNA_BIN"] != first["DYNA_BIN"] {
		t.Fatalf("publication changed: %s, then %s", first["DYNA_BIN"], second["DYNA_BIN"])
	}
	get := exec.Command(git, "config", "--global", "--get", "user.name")
	get.Env = merged(env, second)
	if out, err := get.Output(); err != nil || strings.TrimSpace(string(out)) != "Tool Wrote This" {
		t.Fatalf("the tool's write was lost: %q %v", out, err)
	}
}

// A later launch with another owner environment gets its own writable config, so
// a running session keeps the settings it started with.
func TestWritableGitConfigIsPerPublication(t *testing.T) {
	scope, _, env := fixture(t)
	first, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(first["GIT_CONFIG_GLOBAL"])
	if err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(scope, "", append(env, "XDG_CONFIG_HOME=/other/config"))
	if err != nil {
		t.Fatal(err)
	}
	if second["GIT_CONFIG_GLOBAL"] == first["GIT_CONFIG_GLOBAL"] {
		t.Fatal("two publications share one writable Git config")
	}
	after, err := os.ReadFile(first["GIT_CONFIG_GLOBAL"])
	if err != nil || string(after) != string(before) || !strings.Contains(string(after), gitQuote(publishedConfig(first))) {
		t.Fatalf("the first launch's config changed: %q, %v", after, err)
	}
}

// A tool's Git write survives a change of publication: a new launch with another
// PATH or tool list starts from the settings the last one's tools wrote.
func TestGitWriteCarriesToANewPublication(t *testing.T) {
	scope, _, env := fixture(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	first, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	write := exec.Command(git, "config", "--global", "credential.helper", "gh-helper")
	write.Env = merged(env, first)
	if out, err := write.CombinedOutput(); err != nil {
		t.Fatalf("git config --global: %s %v", out, err)
	}
	second, err := Prepare(scope, "", append(env, "XDG_CONFIG_HOME=/other/config"))
	if err != nil {
		t.Fatal(err)
	}
	if second["GIT_CONFIG_GLOBAL"] == first["GIT_CONFIG_GLOBAL"] {
		t.Fatal("the publication did not change")
	}
	get := exec.Command(git, "config", "--global", "--includes", "--get", "credential.helper")
	get.Env = merged(env, second)
	if out, err := get.Output(); err != nil || strings.TrimSpace(string(out)) != "gh-helper" {
		t.Fatalf("the earlier write is missing from the new launch: %q %v", out, err)
	}
}

// D-292: a launch that returns to an older publication gets the Git settings that
// tools wrote under a newer publication while it was away.
func TestGitWriteFromANewerPublicationReachesAnOlderOne(t *testing.T) {
	scope, _, env := fixture(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	older, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := Prepare(scope, "", append(env, "XDG_CONFIG_HOME=/other/config"))
	if err != nil {
		t.Fatal(err)
	}
	write := exec.Command(git, "config", "--global", "credential.helper", "gh-helper")
	write.Env = merged(env, newer)
	if out, err := write.CombinedOutput(); err != nil {
		t.Fatalf("git config --global: %s %v", out, err)
	}
	back, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	if back["GIT_CONFIG_GLOBAL"] != older["GIT_CONFIG_GLOBAL"] {
		t.Fatalf("the launch did not return to the older publication: %s", back["GIT_CONFIG_GLOBAL"])
	}
	get := exec.Command(git, "config", "--global", "--includes", "--get", "credential.helper")
	get.Env = merged(env, back)
	if out, err := get.Output(); err != nil || strings.TrimSpace(string(out)) != "gh-helper" {
		t.Fatalf("the newer write is missing from the older publication: %q %v", out, err)
	}
}

// D-292: when the newer publication changes or removes a Git setting, returning to
// the older publication must show that change, with no stale or duplicate value.
func TestGitWriteOverridesAndRemovesInAnOlderPublication(t *testing.T) {
	scope, _, env := fixture(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	older, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	gitWrite := func(launch map[string]string, args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"config", "--global"}, args...)...)
		cmd.Env = merged(env, launch)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config --global %v: %s %v", args, out, err)
		}
	}
	gitWrite(older, "credential.helper", "old")
	gitWrite(older, "user.name", "Old Name")
	newer, err := Prepare(scope, "", append(env, "XDG_CONFIG_HOME=/other/config"))
	if err != nil {
		t.Fatal(err)
	}
	gitWrite(newer, "credential.helper", "new")
	gitWrite(newer, "--unset", "user.name")
	back, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	values := exec.Command(git, "config", "--global", "--includes", "--get-all", "credential.helper")
	values.Env = merged(env, back)
	if out, err := values.Output(); err != nil || strings.TrimSpace(string(out)) != "new" {
		t.Fatalf("older publication has stale or duplicate helpers: %q %v", out, err)
	}
	name := exec.Command(git, "config", "--global", "--includes", "--get", "user.name")
	name.Env = merged(env, back)
	if out, _ := name.Output(); strings.TrimSpace(string(out)) == "Old Name" {
		t.Fatalf("a removed setting came back: %q", out)
	}
}
