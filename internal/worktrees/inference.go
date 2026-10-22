package worktrees

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

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

const (
	inferenceMetadataLimit = 4 << 20
	inferenceLineLimit     = 16 << 20
	inferenceJournalLimit  = 256 << 20
)

// inferOwners reads historical ownership without changing stores or worktrees.
// An error means that ownership is incomplete; callers must keep the trees.
// runHistory caches what the journals of ended workflow runs say about
// trees. An ended run never changes, and reading its journals is most of an
// inference pass. Sessions and running workflows are always read again.
type runHistory struct {
	read map[string]bool       // ended run IDs whose journals were read
	refs map[string][]ownerRef // tree path -> owners found in those journals
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
	runs, err := runstore.ListAll()
	if err != nil {
		return nil, fmt.Errorf("infer run owners: %w", err)
	}
	metas := make(map[string]runstore.Meta, len(runs))
	for _, m := range runs {
		metas[m.ID] = m
	}
	visitRun := func(root *os.Root, name string) error {
		// A run folder with no row, such as a broken or starting run, is an
		// open owner, so its trees are kept.
		meta, ok := metas[name]
		if !ok {
			reportRowless(name)
		}
		ref := ownerRef{ID: name, Address: name, Closed: ok && meta.Status != "running" && !meta.EndedAt.IsZero()}
		cached = history != nil && ref.Closed
		if cached && history.read[meta.ID] {
			return nil
		}
		if cached {
			history.read[meta.ID] = true
		}
		dirs, found, err := inferenceDirs(ctx, root, filepath.Join(name, runstore.DirsFile))
		if err != nil {
			return err
		}
		if !found {
			// TODO: legacy/shim remove later — runs started before runstore kept dirs.jsonl have only their journals. An ended run gets its list built once. Remove when no live or archived run lacks dirs.jsonl.
			if dirs, err = inferenceJournalDirs(ctx, root, name); err != nil {
				return err
			}
			if ref.Closed {
				// Best effort: a failed write only means the next pass reads the journals again.
				_ = inferenceWriteDirs(root, name, dirs)
			}
		}
		for _, dir := range dirs {
			if err := match(dir, ref, false); err != nil {
				return err
			}
		}
		return nil
	}
	for _, root := range []string{runstore.RunsDir(), runstore.ArchiveDir()} {
		if err := inferenceStore(ctx, root, visitRun); err != nil {
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

func inferenceOpen(root *os.Root, path string) (*os.File, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("ownership file is not regular: %s", path)
	}
	// Nonblocking open also prevents a concurrent replacement with a FIFO
	// from trapping inference before the regular-file check.
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("unsafe ownership file: %s", path)
	}
	return file, nil
}

type inferenceReader struct {
	ctx context.Context
	r   io.Reader
}

func (r inferenceReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
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

func inferenceMetadata(ctx context.Context, root *os.Root, path string, dest any) (bool, error) {
	file, err := inferenceOpen(root, path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(inferenceReader{ctx, file}, inferenceMetadataLimit+1))
	if err != nil {
		return false, err
	}
	if len(data) > inferenceMetadataLimit {
		return false, fmt.Errorf("ownership metadata exceeds size limit: %s", path)
	}
	if err := inferenceDecode(data, dest); err != nil {
		return false, fmt.Errorf("invalid ownership metadata %s: %w", path, err)
	}
	return true, nil
}

func inferenceDecode(data []byte, dest any) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("expected a JSON object")
	}
	return json.Unmarshal(data, dest)
}

func inferenceJournal(ctx context.Context, root *os.Root, path string, visit func(string, string) error) error {
	file, err := inferenceOpen(root, path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	limited := &io.LimitedReader{R: inferenceReader{ctx, file}, N: inferenceJournalLimit + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), inferenceLineLimit)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		// Omit Result and other large fields: Unmarshal validates them without
		// allocating their decoded object graphs.
		var entry struct{ Dir, Cwd string }
		if err := inferenceDecode(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("invalid ownership journal %s: %w", path, err)
		}
		if err := visit(entry.Dir, entry.Cwd); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read ownership journal %s: %w", path, err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("ownership journal exceeds size limit: %s", path)
	}
	return ctx.Err()
}

// inferenceDirs reads a run's worker directory list. found is false when the
// run has none.
func inferenceDirs(ctx context.Context, root *os.Root, path string) (dirs []string, found bool, err error) {
	file, err := inferenceOpen(root, path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(inferenceReader{ctx, file}, inferenceMetadataLimit+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > inferenceMetadataLimit {
		return nil, false, fmt.Errorf("worker directory list exceeds size limit: %s", path)
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var dir string
		if err := json.Unmarshal(line, &dir); err != nil {
			return nil, false, fmt.Errorf("invalid worker directory list %s: %w", path, err)
		}
		dirs = append(dirs, dir)
	}
	return dirs, true, nil
}

// inferenceJournalDirs collects the directories a run's journals name.
func inferenceJournalDirs(ctx context.Context, root *os.Root, run string) ([]string, error) {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir != "" && !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	for _, filename := range []string{"journal.jsonl", "events.jsonl"} {
		if err := inferenceJournal(ctx, root, filepath.Join(run, filename), func(dir, cwd string) error {
			add(dir)
			add(cwd)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return dirs, nil
}

// inferenceWriteDirs publishes an ended run's directory list in one rename,
// so a concurrent reader never sees part of it.
func inferenceWriteDirs(root *os.Root, run string, dirs []string) error {
	var b bytes.Buffer
	for _, dir := range dirs {
		line, err := json.Marshal(dir)
		if err != nil {
			return err
		}
		b.Write(append(line, '\n'))
	}
	// Each writer owns its temporary file, so a concurrent pass can never
	// truncate a file that another pass is about to rename.
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := filepath.Join(run, runstore.DirsFile+".tmp-"+hex.EncodeToString(suffix[:]))
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = file.Write(b.Bytes())
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(tmp, filepath.Join(run, runstore.DirsFile))
	}
	if err != nil {
		_ = root.Remove(tmp)
	}
	return err
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
