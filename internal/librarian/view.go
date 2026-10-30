package librarian

import (
	"context"
	"errors"
	"os"

	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/library"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/settings"
)

// View returns the live map of a repository with its stale ids and lint
// findings. Both read the librarian's checkout of main when it exists.
func View(ctx context.Context, dataDir, repoDir string) (LibraryView, error) {
	v := LibraryView{Entries: []library.Entry{}, Stale: []string{}, Lint: []library.Finding{}}
	r, err := repoid.Resolve(ctx, repoDir)
	if err != nil {
		return v, err
	}
	v.Repo = r.Main
	set, err := settings.Load(settings.DefaultPath())
	if err != nil {
		return v, err
	}
	if st, err := (librarianstate.Store{DataDir: dataDir}).Get(r.ID); err == nil {
		v.Enabled, v.Head, v.LastRun = set.LibraryOn(r.Main), st.Head, st.LastRun
	} else if !errors.Is(err, os.ErrNotExist) {
		return v, err
	}
	entries, err := library.Store{DataDir: dataDir}.List(r.ID, false)
	if err != nil {
		return v, err
	}
	if len(entries) > 0 {
		v.Entries = entries
	}
	dir := r.Main
	if tree := TreePath(dataDir, r.ID); exists(tree) {
		dir = tree
	}
	stale, err := library.Stale(ctx, dir, entries)
	if err != nil {
		return v, err
	}
	for _, e := range entries {
		if stale[e.ID] {
			v.Stale = append(v.Stale, e.ID)
		}
	}
	if v.Lint, err = library.Lint(ctx, dir, entries, set.LibraryReadmeMaxLines); err != nil {
		return v, err
	}
	if v.Lint == nil {
		v.Lint = []library.Finding{}
	}
	return v, nil
}

func exists(dir string) bool {
	_, err := os.Stat(dir)
	return err == nil
}
