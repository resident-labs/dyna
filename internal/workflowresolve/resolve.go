// Package workflowresolve locates workflow scripts without evaluating them.
package workflowresolve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Error struct {
	Ref, Path, Registry, Examples string
	Cause                         error
}

func (e *Error) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("read nested workflow %s: %v", e.Path, e.Cause)
	}
	if strings.TrimSpace(e.Ref) == "" {
		return "workflow reference must not be empty"
	}
	return fmt.Sprintf("workflow %q was not found as a script path or in %s or %s", e.Ref, e.Registry, e.Examples)
}

func (e *Error) Unwrap() error {
	if e.Cause != nil {
		return e.Cause
	}
	return os.ErrNotExist
}

func registryDir(dir string) string {
	if dir != "" {
		return dir
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "dyna", "workflows")
}

// Resolve searches script directory, work directory, registry, then examples.
func Resolve(ref, scriptPath, workDir, workflowDir string) (string, string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", "", &Error{Ref: ref, Registry: registryDir(workflowDir), Examples: filepath.Join(workDir, "examples")}
	}
	var candidates []string
	seen := map[string]bool{}
	add := func(base string) {
		if base == "" {
			return
		}
		for _, candidate := range []string{base, base + ".js"} {
			if filepath.Ext(base) == ".js" && candidate != base {
				continue
			}
			abs, err := filepath.Abs(candidate)
			if err == nil && !seen[abs] {
				seen[abs] = true
				candidates = append(candidates, abs)
			}
		}
	}
	if filepath.IsAbs(ref) {
		add(ref)
	} else {
		if scriptPath != "" {
			add(filepath.Join(filepath.Dir(scriptPath), ref))
		}
		add(filepath.Join(workDir, ref))
		if filepath.Base(ref) == ref {
			registry := registryDir(workflowDir)
			add(filepath.Join(registry, ref))
			add(filepath.Join(workDir, "examples", ref))
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(candidate)
		if err != nil {
			return "", "", &Error{Ref: ref, Path: candidate, Cause: err}
		}
		return candidate, string(b), nil
	}
	return "", "", &Error{Ref: ref, Registry: registryDir(workflowDir), Examples: filepath.Join(workDir, "examples")}
}
