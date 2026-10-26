package home

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/shellquote"
)

var toolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Prepare returns environment overrides to merge into inherited. It keeps explicit
// tool settings, including empty values. Empty startup inputs disable shell hooks.
// The caller must supply an absolute native scope and the owner's environment.
// realDyna is a trusted absolute executable path; it need not exist yet. The
// bridge still prefers a dyna the inherited PATH installs, because that binary
// is the one an update keeps current (D-171); realDyna is the fallback.
func Prepare(nativeScope, realDyna string, inherited []string) (map[string]string, error) {
	if !filepath.IsAbs(nativeScope) {
		return nil, fmt.Errorf("native scope must be absolute")
	}
	realDyna, err := stableDynaPath(realDyna, nativeScope)
	if err != nil {
		return nil, err
	}
	owner := envMap(inherited)
	home, err := ownerHome(owner)
	if err != nil {
		return nil, err
	}
	config := fallback(owner, "XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path, present := owner["PATH"]
	if !present {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	// The harness and every tool bridge get the owner's login PATH too (D-265).
	path = withLoginPath(path, loginPathSource())
	owner["PATH"] = path
	bridgeRoot := filepath.Join(Home(nativeScope), ".dyna")
	names := []string{"dyna", "git"}
	if extra := owner["DYNA_HOME_TOOLS"]; extra != "" {
		for _, name := range strings.Split(extra, ",") {
			name = strings.TrimSpace(name)
			if !toolName.MatchString(name) || name == "." || name == ".." {
				return nil, fmt.Errorf("unsafe DYNA_HOME_TOOLS name %q", name)
			}
			names = append(names, name)
		}
	}
	resolved := make(map[string]string)
	for _, name := range names {
		if _, ok := resolved[name]; ok {
			continue
		}
		target := ""
		var err error
		if name == "dyna" {
			target, err = dynaBridgeTarget(realDyna, path, bridgeRoot)
		} else {
			target, err = resolve(name, path)
		}
		if err != nil {
			return nil, fmt.Errorf("resolve bridge tool %q: %w", name, err)
		}
		check := target
		if resolvedPath, resolveErr := filepath.EvalSymlinks(target); resolveErr == nil {
			check = resolvedPath
		}
		if rel, relErr := filepath.Rel(bridgeRoot, check); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("bridge tool %q resolves to private bridge", name)
		}
		resolved[name] = target
	}
	out := Roots(nativeScope)
	// Canonical optional policy must win over inherited/profile strings in the
	// caller's final environment merge. Dyna and Git are always enabled.
	optional := make([]string, 0, len(resolved))
	for name := range resolved {
		if name != "dyna" && name != "git" {
			optional = append(optional, name)
		}
	}
	sort.Strings(optional)
	out["DYNA_HOME_TOOLS"] = strings.Join(optional, ",")
	tools, err := ToolRoots(inherited)
	if err != nil {
		return nil, err
	}
	for key, value := range tools {
		out[key] = value
	}
	if value, ok := owner["GH_CONFIG_DIR"]; ok {
		out["GH_CONFIG_DIR"] = value
	} else {
		out["GH_CONFIG_DIR"] = filepath.Join(config, "gh")
	}
	if err := Ensure(nativeScope); err != nil {
		return nil, err
	}
	if err := removeWorktreesLink(nativeScope); err != nil {
		return nil, err
	}
	if out[OwnerEnvKey], err = ownerRecord(nativeScope, resolved["dyna"], owner); err != nil {
		return nil, err
	}
	files := map[string][]byte{"gitconfig": []byte(globalConfig(owner, home, config))}
	for name, target := range resolved {
		files["bin/"+name] = []byte(bridge(owner, target, name == "dyna"))
	}
	publication, err := publish(bridgeRoot, files)
	if err != nil {
		return nil, err
	}
	bin := filepath.Join(publication, "bin")
	out["PATH"] = bin + string(os.PathListSeparator) + path
	out["DYNA_BIN"] = filepath.Join(bin, "dyna")
	// Tools write the global Git config (gh auth setup-git, git config --global),
	// so GIT_CONFIG_GLOBAL is a writable file that includes the immutable one.
	// One file per publication, created once and never rewritten: a running
	// session keeps its settings, and a tool's write is never lost to a rewrite.
	writable := filepath.Join(Home(nativeScope), ".dyna-gitconfig-"+filepath.Base(publication))
	if err := createWritableGitConfig(writable, filepath.Join(publication, "gitconfig")); err != nil {
		return nil, err
	}
	if err := syncToolSettings(writable); err != nil {
		return nil, err
	}
	out["GIT_CONFIG_GLOBAL"] = writable
	return out, nil
}

// removeWorktreesLink deletes the symlink older builds put in the private HOME.
// A real directory is left alone: Prepare never moves or deletes worktrees.
// TODO: legacy/shim remove later — drops the .dyna-worktrees link from private homes made by older builds; remove once those homes are gone.
func removeWorktreesLink(nativeScope string) error {
	link := filepath.Join(Home(nativeScope), ".dyna-worktrees")
	if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink != 0 {
		// A concurrent Prepare may have removed it already.
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ToolRoots returns owner toolchain roots and disabled shell startup inputs.
// It does not create files, change HOME/XDG, or expose Git/gh configuration.
// Explicit inherited values, including empty values, take precedence.
func ToolRoots(inherited []string) (map[string]string, error) {
	owner := envMap(inherited)
	home, err := ownerHome(owner)
	if err != nil {
		return nil, err
	}
	config := fallback(owner, "XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	data := fallback(owner, "XDG_DATA_HOME", filepath.Join(home, ".local/share"))
	cache := fallback(owner, "XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	path, present := owner["PATH"]
	if !present {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	out := make(map[string]string)
	defaults := map[string]string{
		"MISE_DATA_DIR": filepath.Join(data, "mise"), "MISE_CONFIG_DIR": filepath.Join(config, "mise"), "MISE_CACHE_DIR": filepath.Join(cache, "mise"),
		"RUSTUP_HOME": filepath.Join(home, ".rustup"), "CARGO_HOME": filepath.Join(home, ".cargo"), "GOPATH": filepath.Join(home, "go"),
		"GOMODCACHE": filepath.Join(home, "go/pkg/mod"), "GOCACHE": filepath.Join(cache, "go-build"),
		"NPM_CONFIG_USERCONFIG": filepath.Join(home, ".npmrc"), "BUN_INSTALL": filepath.Join(home, ".bun"), "PNPM_HOME": filepath.Join(data, "pnpm"),
	}
	if value := owner["GOPATH"]; value != "" {
		defaults["GOMODCACHE"] = filepath.Join(strings.Split(value, string(os.PathListSeparator))[0], "pkg/mod")
	}
	// Query Go with owner roots before installing bridges. Its output and runtime
	// are bounded; unavailable or broken Go falls back to known owner paths.
	needGo := false
	for _, key := range []string{"GOPATH", "GOMODCACHE", "GOCACHE"} {
		if _, ok := owner[key]; !ok {
			needGo = true
		}
	}
	if needGo {
		for key, value := range goDefaults(path, inherited) {
			if value != "" {
				defaults[key] = value
			}
		}
	}
	for key, value := range defaults {
		if explicit, ok := owner[key]; ok {
			out[key] = explicit
		} else {
			out[key] = value
		}
	}
	out["ENV"], out["BASH_ENV"], out["ZDOTDIR"] = "", "", ""
	return out, nil
}

func ownerHome(owner map[string]string) (string, error) {
	if home := owner["HOME"]; home != "" {
		return home, nil
	}
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

func envMap(env []string) map[string]string {
	result := make(map[string]string)
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			result[key] = value
		}
	}
	return result
}
func fallback(env map[string]string, key, def string) string {
	if value := env[key]; value != "" {
		return value
	}
	return def
}
func executable(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("%q is not executable", path)
	}
	return abs, nil
}
func resolve(name, path string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		if target, err := executable(filepath.Join(dir, name)); err == nil {
			return target, nil
		}
	}
	return "", fmt.Errorf("executable not found in inherited PATH")
}

// dynaBridgeTarget picks the path the dyna bridge execs. The dyna the
// owner's PATH installs wins whenever the PATH names one, because an update
// replaces that binary in place: every later exec then starts the current
// build with no engine help, so a session that keeps running across an
// update does not stay on the build its first turn chose until a schema bump
// cuts its shells off (D-171). A bridge another tool home published never
// wins: it follows its own scope's choice of build, not the installation.
// The engine-supplied path, a scope link to the engine's own image, is the
// fallback for an engine whose PATH names no dyna; it follows the engine's
// build instead of the installation.
func dynaBridgeTarget(realDyna, path, bridgeRoot string) (string, error) {
	if realDyna != "" {
		if !filepath.IsAbs(realDyna) {
			return "", fmt.Errorf("realDyna must be absolute")
		}
		check := realDyna
		if resolved, err := filepath.EvalSymlinks(check); err == nil {
			check = resolved
		}
		if rel, err := filepath.Rel(bridgeRoot, check); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("realDyna %q resolves to a private bridge of %s", realDyna, bridgeRoot)
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, "dyna")
		if toolHomeBridge(candidate) {
			continue
		}
		if target, err := executable(candidate); err == nil {
			return target, nil
		}
	}
	if realDyna != "" {
		return filepath.Clean(realDyna), nil
	}
	return "", fmt.Errorf("executable not found in inherited PATH")
}

// toolHomeBridge reports whether path is a bridge some Dyna tool home
// published. Such a wrapper lives under .dyna/tool-home and execs the build
// its own scope chose, so it is not the installed dyna an update keeps
// current (D-171).
func toolHomeBridge(path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == ".dyna" && parts[i+1] == "tool-home" {
			return true
		}
	}
	return false
}

// Launches reports whether a published Dyna command still runs. It follows
// bridge scripts, because a harness that reuses a generation whose image is
// gone starts an MCP server that cannot handshake, and the turn has no Dyna
// tools.
func Launches(path string) bool {
	for depth := 0; depth < 4; depth++ {
		if path == "" {
			return false
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0100 == 0 {
			return false
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		if !strings.HasPrefix(string(raw), "#!") {
			// A real executable needs no further check.
			return true
		}
		target := execTarget(string(raw))
		if target == "" || target == path {
			return true
		}
		path = target
	}
	return false
}

// execTarget returns the path a bridge script execs.
func execTarget(script string) string {
	line := ""
	for _, candidate := range strings.Split(script, "\n") {
		if strings.HasPrefix(candidate, "exec ") {
			line = candidate
			break
		}
	}
	start := strings.IndexByte(line, '\'')
	if start < 0 {
		return ""
	}
	rest := line[start+1:]
	end := strings.IndexByte(rest, '\'')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// stableDynaPath replaces a running image, or a copy of one, with this scope's
// current link and points that link at the image. A bridge, an MCP server
// command and $DYNA_BIN are read again on later turns, so the link is what keeps
// a running session on the engine's current build: naming one build's copy here
// kept a session on the build it first published, and pruning that copy later
// left a wrapper with no executable. /proc/<pid>/exe also stops existing when
// that process exits, which left harness sessions with no Dyna tools.
func stableDynaPath(realDyna, nativeScope string) (string, error) {
	image, copied, err := fsutil.CurrentBuild(realDyna)
	if err != nil || !copied {
		return realDyna, err
	}
	link, err := fsutil.CurrentLink(nativeScope)
	if err != nil {
		return "", err
	}
	if err := fsutil.SetLink(link, image); err != nil {
		return "", err
	}
	return link, nil
}

func bridge(owner map[string]string, target string, isDyna bool) string {
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	// dyna restores the owner roots itself (RestoreOwner), however it starts.
	if !isDyna {
		// Restore PATH too, so nested Prepare calls resolve owner tools, not bridges.
		for _, key := range append([]string{"HOME", "PATH"}, xdgNames...) {
			if value, ok := owner[key]; ok {
				fmt.Fprintf(&script, "export %s=%s\n", key, shellquote.Quote(value))
			} else {
				fmt.Fprintf(&script, "unset %s\n", key)
			}
		}
	}
	fmt.Fprintf(&script, "exec %s \"$@\"\n", shellquote.Quote(target))
	return script.String()
}

func gitConfigHeader(published string) string {
	return "[include]\n\tpath = " + gitQuote(published) + "\n"
}

// syncToolSettings adds to the writable config at self each setting that a tool
// wrote to the config of another publication, oldest config first so the newest
// value stays last. Settings are copied, not included: includes nest at most ten
// deep, and a running session's include must not change. Git's own lock covers
// the write, so a tool writing at the same time is not lost. Writers on two
// publications at once see each other's setting from their next launch.
func syncToolSettings(self string) error {
	dir := filepath.Dir(self)
	matches, _ := filepath.Glob(filepath.Join(dir, ".dyna-gitconfig-*"))
	type other struct {
		path string
		at   time.Time
	}
	var others []other
	for _, match := range matches {
		info, err := os.Lstat(match)
		if match == self || err != nil || !info.Mode().IsRegular() {
			continue
		}
		others = append(others, other{match, info.ModTime()})
	}
	sort.Slice(others, func(i, j int) bool { return others[i].at.Before(others[j].at) })
	have, err := fileSettings(self)
	if err != nil {
		return err
	}
	for _, o := range others {
		settings, err := fileSettings(o.path)
		if err != nil {
			return err
		}
		for _, entry := range settings {
			if slices.Contains(have, entry) {
				continue
			}
			key, value, _ := strings.Cut(entry, "\n")
			if out, err := gitFile(self, "--add", key, value); err != nil {
				return fmt.Errorf("copy Git setting %s: %w: %s", key, err, out)
			}
			have = append(have, entry)
		}
	}
	return nil
}

// fileSettings lists the settings written directly in a config file as
// "key\nvalue" entries, without the include lines Prepare put there.
func fileSettings(path string) ([]string, error) {
	out, err := gitFile(path, "--null", "--list")
	if err != nil {
		return nil, fmt.Errorf("read Git settings of %s: %w", path, err)
	}
	var entries []string
	for _, record := range strings.Split(out, "\x00") {
		key, _, _ := strings.Cut(record, "\n")
		if record == "" || strings.EqualFold(key, "include.path") {
			continue
		}
		entries = append(entries, record)
	}
	return entries, nil
}

// gitFile runs git config on one file with a clean environment.
func gitFile(path string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"config", "--file", path}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "HOME=" + os.DevNull}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// createWritableGitConfig creates path as a Git config that includes the
// published, immutable one. An existing file stays as it is: it may hold what a
// tool wrote, and a running session reads it. The file appears whole or not at
// all, so a concurrent launch never reads half of it.
func createWritableGitConfig(path, published string) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	if err := privateDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".harnesshome-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(gitConfigHeader(published))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0600)
	}
	if err != nil {
		return err
	}
	// Link fails when another launch created the file first; that file stands.
	if err := os.Link(tmp.Name(), path); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}
func gitQuote(value string) string {
	value = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\t", "\\t", "\b", "\\b").Replace(value)
	return "\"" + value + "\""
}
func globalConfig(owner map[string]string, home, config string) string {
	result := "[core]\n\texcludesFile = " + gitQuote(filepath.Join(config, "git/ignore")) + "\n\tattributesFile = " + gitQuote(filepath.Join(config, "git/attributes")) + "\n"
	paths := []string{filepath.Join(config, "git/config"), filepath.Join(home, ".gitconfig")}
	if explicit, ok := owner["GIT_CONFIG_GLOBAL"]; ok {
		paths = nil
		if explicit != "" {
			paths = []string{explicit}
		}
	}
	for _, path := range paths {
		result += "[include]\n\tpath = " + gitQuote(path) + "\n"
	}
	return result
}

// limitedOutput prevents an inherited executable from returning unbounded data.
type limitedOutput struct{ data []byte }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data)+n > 64*1024 {
		return 0, fmt.Errorf("go env output exceeds 64 KiB")
	}
	b.data = append(b.data, p...)
	return n, nil
}

// goProbeConfigHome is a path that is a file, so nothing can be created under it.
const goProbeConfigHome = "/dev/null"

// goProbeEnv is the environment of the `go env` probe. The probe must not write
// into the caller's configuration directory: the toolchain records telemetry
// counters under XDG_CONFIG_HOME/go/telemetry, and in a test that directory is a
// temporary folder, so a late counter write races its removal (D-155).
// GOTELEMETRY=off alone does not stop the local counters in Go 1.26. A temporary
// configuration directory of the probe's own held them, and a late write left
// about 430 folders an hour in /tmp, which is memory. The probe's configuration
// directory is therefore one the toolchain cannot create folders in, so it
// silently records nothing and leaves nothing behind.
//
// The owner's saved Go settings still apply: without GOENV, Go reads
// $XDG_CONFIG_HOME/go/env, so the probe names the caller's file even though it
// uses its own configuration directory for telemetry. A setting saved with
// `go env -w` would otherwise silently become the toolchain default.
func goProbeEnv(env []string) []string {
	// Sorting gives stable, last-value-wins input without reading process env.
	values := envMap(env)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys)+6)
	for _, key := range keys {
		if key == "XDG_CONFIG_HOME" || key == "GOTELEMETRY" || key == "GOENV" {
			continue
		}
		out = append(out, key+"="+values[key])
	}
	if saved := callerGoEnv(values); saved != "" {
		out = append(out, "GOENV="+saved)
	}
	return append(out, "XDG_CONFIG_HOME="+goProbeConfigHome, "GOTELEMETRY=off", "ENV=", "BASH_ENV=", "ZDOTDIR=")
}

// callerGoEnv names the Go settings file the caller's environment selects: an
// explicit GOENV, else the toolchain's default under the caller's configuration
// directory. An empty result leaves the choice to the toolchain.
func callerGoEnv(values map[string]string) string {
	if saved := values["GOENV"]; saved != "" {
		return saved
	}
	if dir := values["XDG_CONFIG_HOME"]; dir != "" {
		return filepath.Join(dir, "go", "env")
	}
	if home := values["HOME"]; home != "" {
		return filepath.Join(home, ".config", "go", "env")
	}
	return ""
}

func goDefaults(path string, env []string) map[string]string {
	target, err := resolve("go", path)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, target, "env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE")
	cmd.Env = goProbeEnv(env)
	cmd.WaitDelay = 100 * time.Millisecond
	var output limitedOutput
	cmd.Stdout = &output
	if cmd.Run() != nil {
		return nil
	}
	var result map[string]string
	if json.Unmarshal(output.data, &result) != nil {
		return nil
	}
	for key := range result {
		if key != "GOPATH" && key != "GOMODCACHE" && key != "GOCACHE" {
			delete(result, key)
		}
	}
	return result
}
