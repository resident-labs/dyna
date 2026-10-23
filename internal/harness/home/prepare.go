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
// realDyna is a trusted absolute executable path; it need not exist yet.
// Leave it empty to resolve and validate dyna using inherited PATH.
func Prepare(nativeScope, realDyna string, inherited []string) (map[string]string, error) {
	if !filepath.IsAbs(nativeScope) {
		return nil, fmt.Errorf("native scope must be absolute")
	}
	realDyna = stableDynaPath(realDyna)
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
		if name == "dyna" && realDyna != "" {
			if !filepath.IsAbs(realDyna) {
				return nil, fmt.Errorf("realDyna must be absolute")
			}
			target = filepath.Clean(realDyna)
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
	out["GIT_CONFIG_GLOBAL"] = filepath.Join(publication, "gitconfig")
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

// stableDynaPath replaces a running-image path with a copy of the same build
// that outlives its process. A bridge, an MCP server command and $DYNA_BIN are
// read again on later turns; /proc/<pid>/exe stops existing when that process
// exits, which left harness sessions with no Dyna tools.
func stableDynaPath(realDyna string) string {
	if !strings.HasPrefix(realDyna, "/proc/") {
		return realDyna
	}
	stable, err := fsutil.StableImage(realDyna)
	if err != nil || !filepath.IsAbs(stable) {
		return realDyna
	}
	return stable
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
func goDefaults(path string, env []string) map[string]string {
	target, err := resolve("go", path)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, target, "env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE")
	// Sorting gives stable, last-value-wins input without reading process env.
	values := envMap(env)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		cmd.Env = append(cmd.Env, key+"="+values[key])
	}
	cmd.Env = append(cmd.Env, "ENV=", "BASH_ENV=", "ZDOTDIR=")
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
