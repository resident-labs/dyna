package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/store"
)

// TODO: legacy/shim remove later — the previous release reads each moved
// store's JSON file and writes it too, so a downgrade keeps every record
// (requirement 24) and an old binary's write reaches dyna.db. Mirror,
// ImportIfForeign, ForgetMirrors and the state_mirrors table go with item 31,
// when no binary from before dyna.db is alive.

// A stamp names the copy that Mirror last wrote for a key: version is the
// copy's version, and pending is the file ID (device and inode) of a copy
// Mirror is renaming into place, until it is stamped. A file is ours when it
// matches either one; an old binary writes a new file and renames it, so its
// file matches neither. Keys start with their store, such as
// "turns/<session>/<n>", so two stores never share one. A stamp left for a
// deleted record is harmless: it matters only while a file exists at the
// key's path. Stores still drop them with ForgetMirrors when they delete
// records.
const mirrorSchema = `CREATE TABLE IF NOT EXISTS state_mirrors (
 key TEXT PRIMARY KEY,
 version TEXT NOT NULL,
 pending TEXT
)`

// MigrateMirrors creates the stamp table. Only the engine runs it, with its
// other stores (item 41), so a process that runs before the engine has
// migrated fails clearly instead of changing the schema.
func MigrateMirrors(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "state.mirrors", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), mirrorSchema)
		return err
	})
}

type mirrorStamp struct{ version, pending string }

// owns reports whether info is a copy that Mirror wrote. An empty version,
// on a platform that cannot tell rapid replacements apart, never matches.
func (s mirrorStamp) owns(info os.FileInfo) bool {
	version, id := store.VersionOf(info).Key(), fileID(info)
	return version != "" && version == s.version || id != "" && id == s.pending
}

type stampQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readStamp(ctx context.Context, q stampQuerier, key string) (mirrorStamp, error) {
	var s mirrorStamp
	var pending sql.NullString
	err := q.QueryRowContext(ctx, "SELECT version, pending FROM state_mirrors WHERE key = ?", key).Scan(&s.version, &pending)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	s.pending = pending.String
	return s, err
}

// setStamp records version as key's copy and clears pending.
func setStamp(ctx context.Context, conn *sql.Conn, key, version string) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO state_mirrors(key, version, pending) VALUES (?, ?, NULL)
ON CONFLICT(key) DO UPDATE SET version = excluded.version, pending = NULL`, key, version)
	return err
}

// fileID is info's device and inode, or empty where the platform has none.
func fileID(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return ""
}

// mirrorStep runs at Mirror's two crash points: "pending", after the pending
// stamp and before the rename, and "renamed", after the rename and before
// the final stamp. Tests replace it to stop Mirror there, as a crash would,
// or to put another writer's file in that gap.
var mirrorStep = func(step, path string) error { return nil }

// Mirror writes the previous release's copy of a record to path, after the
// record's commit. Call it after Write returns, never inside a Write fn:
// mirrorMu is taken before the writer lock. write must write the record as
// it is committed now, so a late Mirror never writes older content.
//
// The order is commit, then the copy, then its stamp. Mirror owns the
// temporary file and the rename: it records the temporary file as pending,
// renames it onto path, then stamps its version and clears pending. A crash
// before the rename leaves the old copy, which matches the stamp; a crash
// after it leaves our copy, which matches pending. Either way the record
// wins. Mirror never renames over a file another binary wrote: it leaves
// that file for ImportIfForeign. So a store calls ImportIfForeign before its
// own read-modify-write, then writes, then calls Mirror; if it committed
// first, the next import would write the old binary's older content over the
// new record. mirrorMu orders one process only: a store whose record is
// written from several processes (the CLI and the engine) holds its own
// record lock across ImportIfForeign, its Write and Mirror, as turns hold the
// session lock. A writer that edits the file in place, keeping its inode, is
// not detected; old binaries write by rename.
func (w *Writer) Mirror(ctx context.Context, key, path string, write func(io.Writer) error) error {
	w.mirrorMu.Lock()
	defer w.mirrorMu.Unlock()
	// Refuse a directory or symlink before any temporary file or pending
	// stamp: a pending inode left behind could later match another binary's
	// file, since inode numbers are reused.
	if err := refuseNonRegular(path); err != nil {
		return err
	}
	tmp, ours, err := writeTemp(path, write)
	if tmp != "" {
		defer os.Remove(tmp) // Gone after the rename.
	}
	if err != nil {
		return err
	}
	var stamp mirrorStamp
	if err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		var err error
		if stamp, err = readStamp(ctx, conn, key); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO state_mirrors(key, version, pending) VALUES (?, '', ?)
ON CONFLICT(key) DO UPDATE SET pending = excluded.pending`, key, fileID(ours))
		return err
	}); err != nil {
		return err
	}
	if err := mirrorStep("pending", path); err != nil {
		return err
	}
	version := stamp.version
	switch current, err := os.Lstat(path); {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return err
	case err == nil && !current.Mode().IsRegular():
		// It appeared after the first check: clear pending before refusing.
		if err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error { return setStamp(ctx, conn, key, stamp.version) }); err != nil {
			return err
		}
		return fmt.Errorf("%s is not a regular file", path)
	case err == nil && !stamp.owns(current):
		// Another binary wrote path: keep its file, unstamped.
	default:
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
		if err := mirrorStep("renamed", path); err != nil {
			return err
		}
		now, err := os.Lstat(path)
		if err != nil {
			return err
		}
		// Another writer renamed its file onto path after us: leave it
		// unstamped, so ImportIfForeign reads it.
		if os.SameFile(ours, now) {
			version = store.VersionOf(now).Key()
		}
	}
	return w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error { return setStamp(ctx, conn, key, version) })
}

// refuseNonRegular fails when path exists and is not a regular file.
func refuseNonRegular(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}

// writeTemp writes and syncs a temporary file beside path. It returns the
// file's name, even on failure, and its info.
func writeTemp(path string, write func(io.Writer) error) (string, os.FileInfo, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".mirror-*")
	if err != nil {
		return "", nil, err
	}
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	var info os.FileInfo
	if err == nil {
		info, err = f.Stat()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return f.Name(), info, err
}

// Foreign reports whether path holds a file that another binary wrote: it
// exists and is not a copy that Mirror wrote for key. It takes no lock and
// writes nothing, so a store can check cheaply before it takes its record
// lock for ImportIfForeign.
func (w *Writer) Foreign(ctx context.Context, key, path string) (bool, error) {
	info, err := importSource(ctx, w.db, key, path)
	return info != nil, err
}

// MirrorVersion returns the completed copy's version, or empty when the
// file is foreign, missing, or still has a pending rename/stamp.
// TODO: legacy/shim remove later — identifies completed previous-release copies; remove with Mirror and its callers.
func (w *Writer) MirrorVersion(ctx context.Context, key, path string) (string, error) {
	info, err := regularFile(path)
	if err != nil || info == nil {
		return "", err
	}
	stamp, err := readStamp(ctx, w.db, key)
	if err != nil {
		return "", err
	}
	if stamp.pending != "" || !stamp.owns(info) {
		return "", nil
	}
	return stamp.version, nil
}

// ImportIfForeign reads path into state when another binary wrote it: the
// file exists and is not a copy that Mirror wrote. Call it after Write
// returns, never inside a Write fn: mirrorMu is taken before the writer
// lock. read stores raw on conn, in the same transaction that saves the new
// stamp, and must use only conn. The stamp is checked again inside that
// transaction, which also reads the file, under the writer lock, so two
// racing readers import once. path must be a regular file, not a symlink.
// It reports whether it imported.
//
// read returns ErrSkipForeign for a file it cannot use, such as one that is
// not valid JSON. ImportIfForeign then keeps the record as it is, logs the
// file once and stamps its version, so it is not tried again. A later write
// by another binary lands as a new file and is imported.
func (w *Writer) ImportIfForeign(ctx context.Context, key, path string, read func(conn *sql.Conn, raw []byte) error) (bool, error) {
	if foreign, err := w.Foreign(ctx, key, path); err != nil || !foreign {
		return false, err
	}
	w.mirrorMu.Lock()
	defer w.mirrorMu.Unlock()
	imported := false
	err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		info, err := importSource(ctx, conn, key, path)
		if err != nil || info == nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		err = read(conn, raw)
		skipped := errors.Is(err, ErrSkipForeign)
		if err != nil && !skipped {
			return err
		}
		if err := setStamp(ctx, conn, key, store.VersionOf(info).Key()); err != nil {
			return err
		}
		if skipped {
			w.logf("state: skipped %s for %s: %v", path, key, err)
			return nil
		}
		imported = true
		return nil
	})
	if errors.Is(err, ErrRecordExists) {
		return false, nil
	}
	return imported, err
}

// ErrRecordExists is what an ImportIfForeign read returns when it imports
// only a missing record and finds the record there. ImportIfForeign then
// writes nothing and leaves the file unstamped, so a later import under the
// record's lock still reads it.
var ErrRecordExists = errors.New("the record exists")

// ErrSkipForeign is what an ImportIfForeign read returns for a file it
// cannot use. Wrap it to say why.
var ErrSkipForeign = errors.New("the file was skipped")

// regularFile is path's info, or nil when there is no file.
func regularFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return info, nil
}

// ForgetMirrors drops the stamps of key and of every key under key + "/" in
// the caller's transaction, when a store deletes those records.
func ForgetMirrors(ctx context.Context, conn *sql.Conn, key string) error {
	_, err := conn.ExecContext(ctx, "DELETE FROM state_mirrors WHERE key = ? OR substr(key, 1, ?) = ?", key, len(key)+1, key+"/")
	return err
}
