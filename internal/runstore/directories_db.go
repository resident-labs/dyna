package runstore

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const directoriesSchema = `CREATE TABLE run_directories (
 run_id TEXT PRIMARY KEY, body TEXT NOT NULL CHECK(json_valid(body) AND json_type(body)='array'),
 complete INTEGER NOT NULL DEFAULT 0 CHECK(complete IN (0,1))
)`

// DirectorySet is the canonical ownership evidence for one run. An incomplete
// set cannot prove that a worktree has no owner.
type DirectorySet struct {
	Directories []string
	Complete    bool
}

func directoryPaths(dirs []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) || strings.ContainsRune(dir, 0) {
			return nil, fmt.Errorf("run directory must be an absolute path")
		}
		dir = filepath.Clean(dir)
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out, nil
}

func readDirectories(ctx context.Context, q state.Reader, id string) (DirectorySet, bool, error) {
	var raw string
	var set DirectorySet
	err := q.QueryRowContext(ctx, "SELECT body, complete FROM run_directories WHERE run_id=?", id).Scan(&raw, &set.Complete)
	if errors.Is(err, sql.ErrNoRows) {
		return set, false, nil
	}
	if err != nil {
		return set, false, err
	}
	if err := json.Unmarshal([]byte(raw), &set.Directories); err != nil || set.Directories == nil {
		return DirectorySet{}, false, fmt.Errorf("run %s directory set must be an array of paths", id)
	}
	set.Directories, err = directoryPaths(set.Directories)
	return set, true, err
}

func writeDirectories(ctx context.Context, conn *sql.Conn, id string, dirs []string, complete bool) error {
	dirs, err := directoryPaths(dirs)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(dirs)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO run_directories(run_id,body,complete) VALUES(?,?,?)
ON CONFLICT(run_id) DO UPDATE SET body=excluded.body, complete=excluded.complete`, id, string(raw), complete)
	return err
}

// AddDirectoriesTx adds ownership evidence to a run's current set inside the
// caller's state transaction. A complete set is sealed and refuses additions.
func AddDirectoriesTx(ctx context.Context, conn *sql.Conn, id string, dirs []string) error {
	if err := validateRunID(id); err != nil {
		return err
	}
	set, _, err := readDirectories(ctx, conn, id)
	if err != nil {
		return err
	}
	if set.Complete {
		return fmt.Errorf("run %s directory set is complete", id)
	}
	return writeDirectories(ctx, conn, id, append(set.Directories, dirs...), false)
}

// ReadDirectories returns a current set. Missing and incomplete sets cannot
// authorize cleanup. The engine has already migrated the store.
func ReadDirectories(ctx context.Context, id string) (DirectorySet, bool, error) {
	if err := validateRunID(id); err != nil {
		return DirectorySet{}, false, err
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		return DirectorySet{}, false, err
	}
	return readDirectories(ctx, db, id)
}

type directoryMigrationEvidence struct {
	dirs  []string
	known bool
}

func migrateDirectories(db *sql.DB, dataDir string) error {
	const key = "runs.directories"
	var version int
	err := db.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if version < 0 {
		return fmt.Errorf("%s has invalid version %d", key, version)
	}
	if version != 0 || !database.OwnsSchema() {
		return database.Migrate(db, key, 1, func(*sql.Conn, int) error { return nil })
	}
	ctx := context.Background()
	all, err := prepareDirectoryMigration(ctx, dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, key, 1, func(conn *sql.Conn, _ int) error {
		if _, err := conn.ExecContext(ctx, directoriesSchema); err != nil {
			return err
		}
		for id, evidence := range all {
			meta, err := LoadStored(ctx, conn, id)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			complete := err == nil && IsFinishedStatus(meta.Status) && !meta.EndedAt.IsZero() && evidence.known
			if err := writeDirectories(ctx, conn, id, evidence.dirs, complete); err != nil {
				return err
			}
		}
		return nil
	})
}

// prepareDirectoryMigration reads retained source files before the shared SQL
// writer is taken. Only validated directory sets reach the commit callback.
func prepareDirectoryMigration(ctx context.Context, dataDir string) (map[string]directoryMigrationEvidence, error) {
	roots := []string{filepath.Join(dataDir, "runs"), filepath.Join(dataDir, "archive", "runs")}
	if archive := store.RunArchiveDir(dataDir); archive != roots[1] {
		roots = append(roots, archive)
	}
	all := map[string]directoryMigrationEvidence{}
	for _, rootPath := range roots {
		root, err := os.OpenRoot(rootPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(rootPath)
		if err != nil {
			root.Close()
			return nil, err
		}
		for _, entry := range entries {
			id := entry.Name()
			// Old archive pointers and unrelated symlinks are not physical runs.
			// Their targets are never read through the live root.
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if !entry.IsDir() || validateRunID(id) != nil {
				continue
			}
			dirs, known, err := directoryEvidence(ctx, root, id)
			if err != nil {
				root.Close()
				return nil, fmt.Errorf("run %s directory evidence: %w", id, err)
			}
			previous, exists := all[id]
			previous.dirs = append(previous.dirs, dirs...)
			previous.known = known && (!exists || previous.known)
			all[id] = previous
		}
		root.Close()
	}
	return all, nil
}

// TODO: legacy/shim remove later
// directoryEvidence is the one eager converter for the retained index and
// source journals. It never changes their bytes or offsets.
func directoryEvidence(ctx context.Context, root *os.Root, id string) ([]string, bool, error) {
	dirs, found, err := directoryFileEvidence(ctx, root, id, DirsFile)
	if err == nil && found {
		return dirs, true, nil
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	dirs = []string{}
	known, incomplete := false, false
	for _, name := range []string{"journal.jsonl", "events.jsonl"} {
		paths, found, err := directoryFileEvidence(ctx, root, id, name)
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		dirs = append(dirs, paths...)
		known = known || found
		incomplete = incomplete || err != nil
	}
	dirs, err = directoryPaths(dirs)
	return dirs, known && !incomplete, err
}

// directoryFileEvidence preserves validated paths even when a later record
// cannot be read. The caller marks such fallback evidence incomplete.
func directoryFileEvidence(ctx context.Context, root *os.Root, id, name string) ([]string, bool, error) {
	dirs := []string{}
	found := false
	readErr := func() error {
		path := filepath.Join(id, name)
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("directory evidence is not a regular file: %s", path)
		}
		f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		actual, err := f.Stat()
		if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
			f.Close()
			return fmt.Errorf("unsafe directory evidence: %s", path)
		}
		found = true
		limited := &io.LimitedReader{R: f, N: 256<<20 + 1}
		scan := bufio.NewScanner(limited)
		scan.Buffer(make([]byte, 64<<10), 16<<20)
		for scan.Scan() {
			if err := ctx.Err(); err != nil {
				f.Close()
				return err
			}
			line := bytes.TrimSpace(scan.Bytes())
			if len(line) == 0 {
				continue
			}
			if name == DirsFile {
				var dir string
				if err := json.Unmarshal(line, &dir); err != nil {
					f.Close()
					return fmt.Errorf("invalid retained directory index: %w", err)
				}
				paths, err := directoryPaths([]string{dir})
				if err != nil {
					f.Close()
					return err
				}
				dirs = append(dirs, paths...)
			} else {
				var entry struct{ Dir, Cwd string }
				if len(line) == 0 || line[0] != '{' {
					f.Close()
					return fmt.Errorf("directory evidence needs a JSON object")
				}
				if err := json.Unmarshal(line, &entry); err != nil {
					f.Close()
					return fmt.Errorf("invalid source journal: %w", err)
				}
				for _, dir := range []string{entry.Dir, entry.Cwd} {
					if dir == "" {
						continue
					}
					paths, err := directoryPaths([]string{dir})
					if err != nil {
						f.Close()
						return err
					}
					dirs = append(dirs, paths...)
				}
			}
		}
		scanErr := scan.Err()
		closeErr := f.Close()
		if scanErr != nil {
			return scanErr
		}
		if closeErr != nil {
			return closeErr
		}
		if limited.N <= 0 {
			return fmt.Errorf("directory evidence exceeds size limit")
		}
		return nil
	}()
	dirs, err := directoryPaths(dirs)
	if readErr != nil {
		return dirs, found, readErr
	}
	return dirs, found, err
}

// FinishWithDirectoriesTx preserves the first terminal state and commits the
// directory completeness bit with that transition. The caller owns receipts.
func FinishWithDirectoriesTx(ctx context.Context, conn *sql.Conn, id, status, errorText string, ended time.Time, complete bool) (Meta, bool, error) {
	if err := validateRunID(id); err != nil {
		return Meta{}, false, err
	}
	if !IsFinishedStatus(status) || ended.IsZero() {
		return Meta{}, false, fmt.Errorf("run finish needs a terminal status and end time")
	}
	meta, changed, err := endRunTx(ctx, conn, id, status, errorText, ended)
	if err != nil || !changed {
		return meta, changed, err
	}
	previous, _, err := readDirectories(ctx, conn, id)
	if err != nil {
		return Meta{}, false, err
	}
	if err := writeDirectories(ctx, conn, id, previous.Directories, complete); err != nil {
		return Meta{}, false, err
	}
	return meta, true, nil
}
