// Package path locates native harness executables without starting them.
package path

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"dyna-agent/internal/profile"
)

// Resolve searches the supplied launch environment's PATH first, followed by
// supported native installation directories. env is the complete effective
// environment, not an override map. The last occurrence of an environment key
// wins, as it does for os/exec. It never runs a binary or sources a shell.
//
// Only Dyna's six native executable names are supported. Custom commands must
// retain their own execution/lookup behavior. A result is an absolute path to
// a regular file with executable permissions (following symlinks).
func Resolve(binary string, env []string) (string, error) {
	return resolveForOS(binary, env, runtime.GOOS)
}

func resolveForOS(binary string, env []string, platform string) (string, error) {
	supported := false
	for _, name := range profile.Harnesses {
		definition, _ := profile.HarnessDefinitionFor(name)
		supported = supported || definition.Binary != "" && definition.Binary == binary
	}
	if !supported {
		return "", fmt.Errorf("unsupported native harness executable %q", binary)
	}
	lookup := func(key string) (string, bool) {
		for i := len(env) - 1; i >= 0; i-- {
			if name, value, ok := strings.Cut(env[i], "="); ok && (name == key || platform == "windows" && strings.EqualFold(name, key)) {
				return value, true
			}
		}
		return "", false
	}
	extensions := []string{""}
	if platform == "windows" {
		pathExt, _ := lookup("PATHEXT")
		if pathExt == "" {
			pathExt = ".COM;.EXE;.BAT;.CMD"
		}
		extensions = nil
		for _, extension := range strings.Split(pathExt, ";") {
			if strings.HasPrefix(extension, ".") && !strings.ContainsAny(extension, "/\\") {
				extensions = append(extensions, strings.ToLower(extension))
			}
		}
	}
	candidate := func(dir string) (string, bool) {
		for _, extension := range extensions {
			path := filepath.Join(dir, binary+extension)
			absolute, err := filepath.Abs(path)
			if err != nil {
				continue
			}
			info, err := os.Stat(absolute)
			if err != nil || !info.Mode().IsRegular() || platform != "windows" && info.Mode().Perm()&0111 == 0 {
				continue
			}
			return absolute, true
		}
		return "", false
	}
	pathEnv, _ := lookup("PATH")
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		if path, ok := candidate(dir); ok {
			// Match Go's refusal to execute a relative PATH result implicitly. Do not
			// silently bypass an explicit PATH match in favor of another installation.
			if !filepath.IsAbs(dir) {
				return "", fmt.Errorf("%s: %w", binary, exec.ErrDot)
			}
			return path, nil
		}
	}
	home, hasHome := lookup("HOME")
	if !hasHome {
		home, _ = os.UserHomeDir()
	}
	if home != "" && filepath.IsAbs(home) {
		dirs := []string{filepath.Join(home, ".local", "bin")}
		if binary == "opencode" {
			dirs = append([]string{filepath.Join(home, ".opencode", "bin")}, dirs...)
		}
		for _, dir := range dirs {
			if path, ok := candidate(dir); ok {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("%s: %w", binary, exec.ErrNotFound)
}
