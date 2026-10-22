package runtime

import (
	"dyna-agent/internal/store"
	"os"
	"path/filepath"
)

// runtimeDataDir resolves aliases in the owner-selected data root before callers
// append owned accounts, generation, or bundle paths. Callers retain strict
// checks below this base. Invalid bases stay unchanged so those checks reject them.
func runtimeDataDir() string {
	data := store.DataDir()
	if canonical, err := canonicalAccountDataRoot(data); err == nil {
		return canonical
	}
	return data
}

// Resolve only the owner-selected data root, including missing final components.
// Descendants below that root remain subject to strict no-symlink checks.
func canonicalAccountDataRoot(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errAccountPath
	}
	missing := []string{}
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", errAccountPath
		}
		// A dangling symlink is not an absent directory.
		if info, e := os.Lstat(current); e == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errAccountPath
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errAccountPath
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// TODO: legacy/shim remove later — saved slots can name the owner's old data
// root alias. Accept an equivalent base only; the owned suffix must stay exact.
func equivalentAccountHome(root, h, id, saved string) bool {
	if !filepath.IsAbs(saved) || filepath.Clean(saved) != saved || filepath.Base(root) != "accounts" {
		return false
	}
	suffix := filepath.Join("accounts", h, id)
	base := saved
	for i := 0; i < 3; i++ {
		base = filepath.Dir(base)
	}
	if filepath.Join(base, suffix) != saved {
		return false
	}
	canonical, err := canonicalAccountDataRoot(base)
	return err == nil && filepath.Join(canonical, suffix) == filepath.Join(root, h, id)
}
