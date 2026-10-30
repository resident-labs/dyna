// Package workflowresolve locates workflow scripts without evaluating them.
package workflowresolve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/workflows/bundled"
)

type Error struct {
	Ref, Path, Registry, Examples string
	Cause                         error
}

func (e *Error) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("read workflow %s: %v", e.Path, e.Cause)
	}
	return fmt.Sprintf("workflow %q was not found", e.Ref)
}
func (e *Error) Unwrap() error {
	if e.Cause != nil {
		return e.Cause
	}
	return os.ErrNotExist
}

func PersonalDir(dir string) string {
	if dir != "" {
		return dir
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "dyna", "workflows")
}

// Resolve accepts explicit paths and otherwise searches project, personal and bundled names.
func Resolve(ref, scriptPath, workDir, workflowDir string) (string, string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", "", &Error{Ref: ref}
	}
	candidates := []string{}
	if filepath.IsAbs(ref) {
		candidates = append(candidates, ref)
	} else {
		if scriptPath != "" {
			candidates = append(candidates, filepath.Join(filepath.Dir(scriptPath), ref))
		}
		candidates = append(candidates, filepath.Join(workDir, ref))
	}
	if scriptPath != "" || filepath.Base(ref) != ref {
		paths := candidates
		candidates = nil
		for _, path := range paths {
			candidates = append(candidates, path)
			if filepath.Ext(path) != ".js" {
				candidates = append(candidates, path+".js")
			}
		}
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return "", "", &Error{Ref: ref, Path: path, Cause: err}
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return "", "", err
			}
			return abs, string(b), nil
		}
	}
	if filepath.Base(ref) != ref || strings.ContainsAny(ref, `/\\`) {
		return "", "", &Error{Ref: ref}
	}
	name := strings.TrimSuffix(ref, ".js")
	if !ValidName(name) {
		return "", "", &Error{Ref: ref}
	}
	for _, dir := range []string{filepath.Join(workDir, ".dyna", "workflows"), PersonalDir(workflowDir)} {
		path := filepath.Join(dir, name+".js")
		b, err := os.ReadFile(path)
		if err == nil {
			return path, string(b), nil
		}
		if !os.IsNotExist(err) {
			return "", "", &Error{Ref: ref, Path: path, Cause: err}
		}
	}
	b, err := bundled.FS.ReadFile(name + ".js")
	if err == nil {
		return "bundled:" + name + ".js", string(b), nil
	}
	return "", "", &Error{Ref: ref}
}

func ValidName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
