package runstore

// Run archive: finished runs move out of RunsDir into ArchiveDir so the run
// catalog (`dyna runs list`, the desktop app) only scans live runs, while every
// id-based read path keeps resolving them.
//
// Layout after archiving run <id>:
//
//	<data>/archive/runs/<id>/   the run directory, byte-identical
//
// Dyna keeps no runs/<id> entry: RunDir resolves an archived id to its archive
// directory directly. A pointer left by an earlier release is removed when the
// run is unarchived or its archive is removed.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/store"
)

// ArchiveDirEnv overrides the archive location (default
// $XDG_DATA_HOME/dyna/archive/runs). It must be set consistently for every
// dyna invocation that should resolve archived ids.
const ArchiveDirEnv = store.RunArchiveDirEnv

// ArchiveDir is where archived run directories live.
func ArchiveDir() string {
	return store.RunArchiveDir(store.DataDir())
}

// StatusInterrupted is the final status of a run that an update broke: it
// started on another build and an agent failed. It can be resumed.
const StatusInterrupted = "interrupted"

// IsFinishedStatus reports whether a run status is terminal and archivable.
func IsFinishedStatus(status string) bool {
	switch status {
	case "ok", "canceled", "error", StatusInterrupted:
		return true
	}
	return false
}

// archivedRunDir returns the archive directory for id when the run is
// archived: its live path holds no directory (absent, or a pointer an earlier
// release left), and the archive holds a real directory. A real live
// directory always wins.
func archivedRunDir(id string) (string, bool) {
	if validateRunID(id) != nil {
		return "", false
	}
	if info, err := os.Lstat(filepath.Join(RunsDir(), id)); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return "", false
		}
	} else if !os.IsNotExist(err) {
		return "", false
	}
	dir := filepath.Join(ArchiveDir(), id)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// IsArchived reports whether id resolves to an archived run.
func IsArchived(id string) bool {
	_, ok := archivedRunDir(id)
	return ok
}

// RunDir resolves a run id to its directory: the live directory under
// RunsDir, or the archive directory for an archived run. Unknown ids return
// the lexical live location so callers see a normal not-exist error.
func RunDir(id string) string {
	if dir, ok := archivedRunDir(id); ok {
		return dir
	}
	return filepath.Join(RunsDir(), id)
}

// ListArchived returns metadata for archived runs, newest first.
func ListArchived() ([]Meta, error) { return listIn(ArchiveDir(), "1") }

// ListAll returns live and archived runs, newest first. A live directory
// shadows an archived copy with the same id.
func ListAll() ([]Meta, error) {
	live, err := List()
	if err != nil {
		return nil, err
	}
	archived, err := ListArchived()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(live))
	out := append([]Meta(nil), live...)
	for _, m := range live {
		seen[m.ID] = true
	}
	for _, m := range archived {
		if !seen[m.ID] {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// ---- selection ----

// ArchiveSelector chooses runs to archive. Explicit IDs and the filters
// combine with AND; with no IDs every live run is a candidate.
type ArchiveSelector struct {
	IDs         []string
	OlderThan   time.Duration // 0: no age filter; age is measured from EndedAt (StartedAt if unset)
	Statuses    []string      // empty: every finished status
	AllFinished bool
	Now         time.Time
}

// ArchiveSkip explains why a run was not selected.
type ArchiveSkip struct {
	ID     string
	Reason string
}

type liveRun struct {
	id      string
	meta    Meta
	hasMeta bool
	paused  bool
}

func (r liveRun) active() bool {
	return !r.hasMeta || r.meta.Status == "running" || r.paused
}

func scanLiveRuns() ([]liveRun, error) {
	db, err := runsDB()
	if err != nil {
		return nil, err
	}
	ids, err := runFolders(db, RunsDir())
	if err != nil {
		return nil, err
	}
	rows, err := metasIn(db, ids, "1")
	if err != nil {
		return nil, err
	}
	var out []liveRun
	for _, id := range ids {
		r := liveRun{id: id}
		r.meta, r.hasMeta = rows[id]
		r.paused = r.meta.Paused
		out = append(out, r)
	}
	return out, nil
}

// ResumedFrom returns authoritative lineage from the shared ancestry store.
// Read errors stop archive decisions.
func ResumedFrom(id string) (string, error) {
	parent, err := (ancestry.Store{DataDir: store.DataDir()}).Parent(context.Background(), RunDir(id))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read resume ancestry for %s: %w", id, err)
	}
	next := strings.TrimSpace(parent)
	if validateRunID(next) != nil {
		return "", nil
	}
	return next, nil
}

// resumeAncestors walks the resumedFrom chain from id (exclusive).
func resumeAncestors(id string) ([]string, error) {
	var out []string
	seen := map[string]bool{id: true}
	for {
		next, err := ResumedFrom(id)
		if err != nil {
			return nil, err
		}
		if next == "" || seen[next] {
			return out, nil
		}
		seen[next] = true
		out = append(out, next)
		id = next
	}
}

// PlanArchive returns the finished live runs sel selects, oldest first, plus
// skip reasons. Never selected: running or paused runs, runs without
// meta (a detached run still starting), and every resume ancestor of
// such an active run. For filter-based selections, resume ancestors of any
// run that stays live are also kept live so a lineage is never split.
func PlanArchive(sel ArchiveSelector) ([]Meta, []ArchiveSkip, error) {
	if !sel.AllFinished && sel.OlderThan <= 0 && len(sel.Statuses) == 0 && len(sel.IDs) == 0 {
		return nil, nil, fmt.Errorf("choose runs to archive: run ids, --older-than, --status, or --all-finished")
	}
	statuses := map[string]bool{}
	for _, s := range sel.Statuses {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !IsFinishedStatus(s) {
			return nil, nil, fmt.Errorf("--status %q: only finished statuses (ok, canceled, error) can be archived", s)
		}
		statuses[s] = true
	}
	now := sel.Now
	if now.IsZero() {
		now = time.Now()
	}
	runs, err := scanLiveRuns()
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]liveRun, len(runs))
	protected := map[string]string{}
	for _, r := range runs {
		byID[r.id] = r
		if r.active() {
			reason := "running"
			switch {
			case !r.hasMeta:
				reason = "starting (no meta yet)"
			case r.paused:
				reason = "paused"
			}
			protected[r.id] = reason
			ancestors, err := resumeAncestors(r.id)
			if err != nil {
				return nil, nil, err
			}
			for _, anc := range ancestors {
				if _, ok := protected[anc]; !ok {
					protected[anc] = "resume ancestor of active run " + r.id
				}
			}
		}
	}

	filtered := func(r liveRun) (bool, string) {
		if !IsFinishedStatus(r.meta.Status) {
			return false, "status " + r.meta.Status + " is not finished"
		}
		if len(statuses) > 0 && !statuses[r.meta.Status] {
			return false, "status " + r.meta.Status + " not selected"
		}
		if sel.OlderThan > 0 {
			ref := r.meta.EndedAt
			if ref.IsZero() {
				ref = r.meta.StartedAt
			}
			if now.Sub(ref) < sel.OlderThan {
				return false, "newer than --older-than"
			}
		}
		return true, ""
	}
	matches := func(r liveRun) (bool, string) {
		if reason, ok := protected[r.id]; ok {
			return false, reason
		}
		return filtered(r)
	}

	var skips []ArchiveSkip
	selected := map[string]bool{}
	if len(sel.IDs) > 0 {
		for _, id := range sel.IDs {
			if err := validateRunID(id); err != nil {
				return nil, nil, err
			}
			r, ok := byID[id]
			if !ok {
				if IsArchived(id) {
					skips = append(skips, ArchiveSkip{id, "already archived"})
				} else {
					skips = append(skips, ArchiveSkip{id, "no such live run"})
				}
				continue
			}
			if ok, reason := matches(r); !ok {
				skips = append(skips, ArchiveSkip{id, reason})
				continue
			}
			selected[id] = true
		}
	} else {
		for _, r := range runs {
			if ok, _ := matches(r); ok {
				selected[r.id] = true
			} else if reason, isProtected := protected[r.id]; isProtected && !r.active() {
				// Report finished runs the filters chose but lineage kept.
				if ok, _ := filtered(r); ok {
					skips = append(skips, ArchiveSkip{r.id, reason})
				}
			}
		}
		// Keep the lineage of every run that stays live.
		for _, r := range runs {
			if selected[r.id] {
				continue
			}
			ancestors, err := resumeAncestors(r.id)
			if err != nil {
				return nil, nil, err
			}
			for _, anc := range ancestors {
				if selected[anc] {
					delete(selected, anc)
					skips = append(skips, ArchiveSkip{anc, "resume ancestor of live run " + r.id})
				}
			}
		}
	}

	out := make([]Meta, 0, len(selected))
	for id := range selected {
		out = append(out, byID[id].meta)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, skips, nil
}

// DirSize sums the apparent size of regular files below dir.
func DirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// ---- archive / unarchive ----

// ArchiveResult describes one archived run.
type ArchiveResult struct {
	ID  string
	Dir string // archive directory
}

// ArchiveRun moves one finished live run into ArchiveDir. The move is a
// single rename on the same filesystem; across filesystems the tree is
// copied to a staging directory, verified byte-for-byte, renamed into place,
// and only then removed from RunsDir. Callers choose runs with PlanArchive,
// which also enforces resume-lineage protection.
func ArchiveRun(id string) (ArchiveResult, error) {
	res := ArchiveResult{ID: id}
	if err := validateRunID(id); err != nil {
		return res, err
	}
	live := filepath.Join(RunsDir(), id)
	info, err := os.Lstat(live)
	if err != nil {
		if os.IsNotExist(err) && IsArchived(id) {
			return res, fmt.Errorf("run %s is already archived", id)
		}
		return res, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if IsArchived(id) {
			return res, fmt.Errorf("run %s is already archived", id)
		}
		return res, fmt.Errorf("run %s is a symlink, not a run directory", id)
	}
	if !info.IsDir() {
		return res, fmt.Errorf("run path %s is not a directory", live)
	}
	meta, err := ReadMeta(id)
	if err != nil {
		return res, fmt.Errorf("run %s: %w", id, err)
	}
	if !IsFinishedStatus(meta.Status) {
		return res, fmt.Errorf("run %s is %s; only finished runs can be archived", id, meta.Status)
	}
	if meta.Paused {
		return res, fmt.Errorf("run %s is paused", id)
	}
	root := ArchiveDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return res, fmt.Errorf("create archive directory: %w", err)
	}
	dest := filepath.Join(root, id)
	if _, err := os.Lstat(dest); err == nil {
		return res, fmt.Errorf("archive already holds %s", dest)
	} else if !os.IsNotExist(err) {
		return res, err
	}
	if err := moveDir(live, dest); err != nil {
		return res, fmt.Errorf("archive %s: %w", id, err)
	}
	if err := bumpRunsRev(); err != nil {
		return res, fmt.Errorf("archive %s: record change: %w", id, err)
	}
	res.Dir = dest
	return res, nil
}

// UnarchiveRun moves an archived run back into RunsDir, removing the pointer
// an earlier release left there. Between removing the pointer and the move,
// RunDir still resolves the id through the archive.
func UnarchiveRun(id string) (string, error) {
	if err := validateRunID(id); err != nil {
		return "", err
	}
	src, ok := archivedRunDir(id)
	if !ok {
		if info, err := os.Lstat(filepath.Join(RunsDir(), id)); err == nil && info.IsDir() {
			return "", fmt.Errorf("run %s is not archived", id)
		}
		return "", fmt.Errorf("no archived run %s in %s", id, ArchiveDir())
	}
	live := filepath.Join(RunsDir(), id)
	if info, err := os.Lstat(live); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("%s exists and is not a pointer to the archive", live)
		}
		if !pointsTo(live, src) {
			return "", fmt.Errorf("%s is a symlink that does not point to %s", live, src)
		}
		if err := os.Remove(live); err != nil {
			return "", fmt.Errorf("remove the pointer an earlier release left: %w", err)
		}
	}
	if err := os.MkdirAll(RunsDir(), 0o755); err != nil {
		return "", err
	}
	if err := moveDir(src, live); err != nil {
		return "", fmt.Errorf("unarchive %s: %w", id, err)
	}
	if err := bumpRunsRev(); err != nil {
		return "", fmt.Errorf("unarchive %s: record change: %w", id, err)
	}
	return live, nil
}

// archivedPointer returns the archive directory for id and checks the live
// entry that a removal would unlink. A symlink there must resolve to that
// archive, so retention never unlinks an entry it cannot prove it owns.
func archivedPointer(id string) (string, error) {
	dir, ok := archivedRunDir(id)
	if !ok {
		return "", nil
	}
	live := filepath.Join(RunsDir(), id)
	info, err := os.Lstat(live)
	if os.IsNotExist(err) {
		return dir, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect %s before removing the archived run: %w", live, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		// A real live directory shadows the archive, so it is not unlinked.
		return dir, nil
	}
	if !pointsTo(live, dir) {
		return "", fmt.Errorf("%s is not a pointer to %s; nothing was removed", live, dir)
	}
	return dir, nil
}

// removeArchived deletes an archived run's directory and, when it is this
// archive's pointer, the live entry an earlier release left.
func removeArchived(id string) error {
	dir, err := archivedPointer(id)
	if err != nil || dir == "" {
		return err
	}
	live := filepath.Join(RunsDir(), id)
	if info, err := os.Lstat(live); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(live); err != nil {
			return err
		}
	}
	return os.RemoveAll(dir)
}

func pointsTo(link, dir string) bool {
	a, err := filepath.EvalSymlinks(link)
	if err != nil {
		return false
	}
	b, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return a == b
}

// renameDir is os.Rename; tests replace it to exercise the cross-filesystem
// path.
var renameDir = os.Rename

func moveDir(src, dst string) error {
	err := renameDir(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	return copyVerifyMove(src, dst)
}

func randomSuffix() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// copyVerifyMove is the cross-filesystem move: copy to a staging directory
// beside dst, verify the copy matches src exactly, rename the staging
// directory into place (same filesystem), then retire src by renaming it
// aside before deleting, so a partial delete never leaves a half run at src.
func copyVerifyMove(src, dst string) error {
	stage := filepath.Join(filepath.Dir(dst), ".staging-"+filepath.Base(dst)+"-"+randomSuffix())
	if err := copyTree(src, stage); err != nil {
		os.RemoveAll(stage)
		return fmt.Errorf("copy: %w", err)
	}
	want, err := treeManifest(src)
	if err != nil {
		os.RemoveAll(stage)
		return fmt.Errorf("verify: %w", err)
	}
	got, err := treeManifest(stage)
	if err != nil {
		os.RemoveAll(stage)
		return fmt.Errorf("verify: %w", err)
	}
	if diff := manifestDiff(want, got); diff != "" {
		os.RemoveAll(stage)
		return fmt.Errorf("verify: copy differs from source: %s", diff)
	}
	if err := os.Rename(stage, dst); err != nil {
		os.RemoveAll(stage)
		return err
	}
	retired := filepath.Join(filepath.Dir(src), ".retired-"+filepath.Base(src)+"-"+randomSuffix())
	if err := os.Rename(src, retired); err != nil {
		os.RemoveAll(dst)
		return fmt.Errorf("retire source: %w", err)
	}
	if err := os.RemoveAll(retired); err != nil {
		return fmt.Errorf("copied and verified, but could not delete %s: %w", retired, err)
	}
	return nil
}

func copyTree(src, dst string) error {
	type dirTimes struct {
		path string
		info fs.FileInfo
	}
	var dirs []dirTimes
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			dirs = append(dirs, dirTimes{target, info})
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(path, target, info)
		default:
			return fmt.Errorf("unsupported file type %s at %s", info.Mode().Type(), rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].info.Mode().Perm()); err != nil {
			return err
		}
		mt := dirs[i].info.ModTime()
		if err := os.Chtimes(dirs[i].path, mt, mt); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, info fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

type manifestEntry struct {
	mode fs.FileMode
	size int64
	sum  string // sha256 for files, link target for symlinks
}

func treeManifest(root string) (map[string]manifestEntry, error) {
	out := map[string]manifestEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		e := manifestEntry{mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if e.sum, err = os.Readlink(path); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			e.size = info.Size()
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			e.sum = hex.EncodeToString(h.Sum(nil))
		}
		out[rel] = e
		return nil
	})
	return out, err
}

func manifestDiff(want, got map[string]manifestEntry) string {
	var b bytes.Buffer
	for rel, w := range want {
		g, ok := got[rel]
		switch {
		case !ok:
			fmt.Fprintf(&b, "missing %s; ", rel)
		case g != w:
			fmt.Fprintf(&b, "mismatch %s; ", rel)
		}
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			fmt.Fprintf(&b, "extra %s; ", rel)
		}
	}
	return strings.TrimSpace(b.String())
}
