package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
)

type ownerRef struct {
	ID, Address string
	Closed      bool
}

type inferredOwners struct {
	Sessions []ownerRef
	Runs     []ownerRef
}

// inferOwners reads historical ownership without changing stores or worktrees.
// An error means that ownership is incomplete; callers must keep the trees.
// runHistory caches completed directory sets. The first terminal transition
// seals them. Sessions and incomplete runs are always checked again.
type runHistory struct {
	read map[string]bool       // run IDs with a complete directory set
	refs map[string][]ownerRef // tree path -> owners in complete directory sets
}

func inferOwners(ctx context.Context, paths []string, history *runHistory) (map[string]inferredOwners, error) {
	if history != nil && history.read == nil {
		history.read, history.refs = map[string]bool{}, map[string][]ownerRef{}
	}
	owners := make(map[string]inferredOwners, len(paths))
	// trees maps each resolved tree to its caller paths. A match walks up the
	// owner's directory, so it costs that path's depth, not one compare per tree.
	trees := make(map[string][]string, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resolved, err := inferenceCanonicalPath(path)
		if err != nil {
			return nil, err
		}
		if _, seen := owners[path]; !seen {
			trees[resolved] = append(trees[resolved], path)
		}
		owners[path] = inferredOwners{}
	}
	if len(trees) == 0 {
		return owners, nil
	}
	cached := false // the run being matched has ended and is recorded in history
	match := func(path string, ref ownerRef, session bool) error {
		if path == "" {
			return nil
		}
		resolved, err := inferenceCanonicalPath(path)
		if err != nil {
			return err
		}
		// resolved is clean and absolute, so its ancestors are exactly the trees that hold it.
		for dir := resolved; ; dir = filepath.Dir(dir) {
			for _, key := range trees[dir] {
				item := owners[key]
				if session {
					item.Sessions = inferenceAddOwner(item.Sessions, ref)
				} else {
					item.Runs = inferenceAddOwner(item.Runs, ref)
					if cached {
						history.refs[key] = inferenceAddOwner(history.refs[key], ref)
					}
				}
				owners[key] = item
			}
			if filepath.Dir(dir) == dir {
				return ctx.Err()
			}
		}
	}
	visitSession := func(name string, raw []byte, err error) error {
		var meta struct {
			sessionlife.Snapshot
			Name, Room, Dir string
			Service         bool
		}
		if err == nil {
			err = json.Unmarshal(raw, &meta)
		}
		if err != nil {
			return fmt.Errorf("invalid ownership metadata %s: %w", name, err)
		}
		if meta.ID != "" && meta.ID != name {
			return errors.New("ownership metadata ID does not match directory")
		}
		meta.ID = name
		// A service runs from a shared directory and never owns a tree.
		if meta.Service {
			return nil
		}
		address := meta.ID
		if meta.Room != "" && meta.Name != "" {
			address = meta.Room + ":" + meta.Name
		}
		return match(meta.Dir, ownerRef{ID: meta.ID, Address: address, Closed: meta.IsClosed()}, true)
	}
	// Every record, closed ones too: a closed owner still proves ownership.
	if err := sessionlife.Each(runstore.SessionsDir(), false, func(name string, raw []byte, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visitSession(name, raw, err); err != nil {
			return fmt.Errorf("ownership entry %s: %w", name, err)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("infer session owners: %w", err)
	}
	runs, err := runstore.StoredMetadata()
	if err != nil {
		return nil, fmt.Errorf("infer run owners: %w", err)
	}
	metas := make(map[string]runstore.Meta, len(runs))
	for _, m := range runs {
		metas[m.ID] = m
	}
	visitRun := func(_ *os.Root, name string) error {
		meta, ok := metas[name]
		if !ok {
			reportRowless(name)
		}
		ref := ownerRef{ID: name, Address: name, Closed: ok && runstore.IsFinishedStatus(meta.Status) && !meta.EndedAt.IsZero()}
		cached = history != nil && ref.Closed
		if cached && history.read[name] {
			return nil
		}
		var set runstore.DirectorySet
		var found bool
		var err error
		if ok {
			set, found, err = runstore.ReadDirectories(ctx, name)
		}
		if err != nil {
			return err
		}
		if !found || !set.Complete {
			// A finished run no longer uses any tree, even if its directories are unknown.
			if ref.Closed {
				return nil
			}
			// Unknown directories of an active or unknown run cannot prove that any tree is unused.
			for key, item := range owners {
				item.Runs = inferenceAddOwner(item.Runs, ref)
				owners[key] = item
			}
			return nil
		}
		if cached {
			history.read[name] = true
		}
		for _, dir := range set.Directories {
			if err := match(dir, ref, false); err != nil {
				return err
			}
		}
		return nil
	}

	for _, meta := range runs {
		if err := visitRun(nil, meta.ID); err != nil {
			return nil, fmt.Errorf("infer stored run owner: %w", err)
		}
	}
	// Physical folders add only rowless owners; canonical owners came from SQL.
	visitRowless := func(root *os.Root, name string) error {
		if _, found := metas[name]; found {
			return nil
		}
		return visitRun(root, name)
	}
	for _, root := range []string{runstore.RunsDir(), runstore.ArchiveDir()} {
		if err := inferenceStore(ctx, root, visitRowless); err != nil {
			return nil, fmt.Errorf("infer run owners: %w", err)
		}
	}
	for key, item := range owners {
		if history != nil {
			for _, ref := range history.refs[key] {
				item.Runs = inferenceAddOwner(item.Runs, ref)
			}
		}
		sort.Slice(item.Sessions, func(i, j int) bool { return item.Sessions[i].ID < item.Sessions[j].ID })
		sort.Slice(item.Runs, func(i, j int) bool { return item.Runs[i].ID < item.Runs[j].ID })
		owners[key] = item
	}
	return owners, nil
}

func inferenceCanonicalPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("ownership path must be absolute")
	}
	resolved, err := resolveAncestors(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve ownership path: %w", err)
	}
	return resolved, nil
}

func inferenceAddOwner(refs []ownerRef, ref ownerRef) []ownerRef {
	for i := range refs {
		if refs[i].ID == ref.ID {
			// Conflicting historical copies must never hide an open owner.
			refs[i].Closed = refs[i].Closed && ref.Closed
			return refs
		}
	}
	return append(refs, ref)
}

// Read stores in batches and confine every open to the store, including races
// that replace an entry with a symlink during inference.
func inferenceStore(ctx context.Context, path string, visit func(*os.Root, string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if path == runstore.RunsDir() {
					// Dyna archives leave a pointer in runs; read the configured archive
					// separately through its own confined root instead of following it.
					target, e := root.Readlink(entry.Name())
					if e == nil {
						if !filepath.IsAbs(target) {
							target = filepath.Join(path, target)
						}
						if filepath.Clean(target) == filepath.Join(runstore.ArchiveDir(), entry.Name()) {
							continue
						}
					}
				}
				return fmt.Errorf("unsafe ownership store entry %q", entry.Name())
			}
			if entry.IsDir() {
				if err := visit(root, entry.Name()); err != nil {
					return fmt.Errorf("ownership entry %s: %w", entry.Name(), err)
				}
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

// rowless holds the run folders already reported for having no row.
var rowless sync.Map

// reportRowless logs, once per process, a run folder that has no row in
// dyna.db, so a broken run is visible instead of silently keeping trees.
func reportRowless(id string) {
	if _, seen := rowless.LoadOrStore(id, true); !seen {
		log.Printf("worktrees: run %s has a folder but no meta in dyna.db; its trees are kept", id)
	}
}

// sessionWorktreePaths selects possible session trees without reading other
// sessions or workflow history. The full inference pass must still check all
// owners of these paths before cleanup can remove anything.
func sessionWorktreePaths(ctx context.Context, paths []string, records map[string]Record, id string) ([]string, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return nil, errors.New("invalid session id")
	}
	var meta struct{ ID, Dir string }
	err := sessionlife.Load(runstore.SessionsDir(), id, &meta)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if meta.ID != "" && meta.ID != id {
		return nil, errors.New("ownership metadata ID does not match directory")
	}
	var dir string
	if meta.Dir != "" {
		dir, err = inferenceCanonicalPath(meta.Dir)
		if err != nil {
			return nil, err
		}
	}
	var selected []string
	for index, path := range paths {
		// The main checkout is protected and cannot need session cleanup.
		if index == 0 {
			continue
		}
		if records[filepath.Clean(path)].Session == id {
			selected = append(selected, path)
			continue
		}
		if dir != "" {
			canonical, err := inferenceCanonicalPath(path)
			if err != nil {
				return nil, err
			}
			if dir == canonical || within(canonical, dir) {
				selected = append(selected, path)
			}
		}
	}
	return selected, ctx.Err()
}
