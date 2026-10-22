package sessionlife

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// TODO: legacy/shim remove later — session records lived in session.json before dyna.db; remove legacyName, legacyLoadRaw, legacyVersion and ImportLegacy once every store has run ImportLegacy (PR 4 of the session record move).
const legacyName = "session.json"

// legacyLoadRaw reads a session.json written before the record moved into
// dyna.db. The read stays inside sessions: no symlink in the path may lead
// out of it, and the file must be private, regular and one JSON object.
func legacyLoadRaw(sessions, id string) ([]byte, Version, error) {
	root, err := os.OpenRoot(sessions)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Join(id, legacyName), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, "", fmt.Errorf("%w: session %s record is a symlink", ErrRecord, id)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
		// os.Root reports a path that escapes sessions; never read past it.
		return nil, "", fmt.Errorf("%w: session %s: %v", ErrRecord, id, err)
	}
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > recordLimit {
		return nil, "", fmt.Errorf("%w: session %s record is not a private regular file", ErrRecord, id)
	}
	raw, err := io.ReadAll(io.LimitReader(f, recordLimit+1))
	if err != nil {
		return nil, "", err
	}
	if !isObject(raw) {
		return nil, "", fmt.Errorf("%w: session %s record is not a JSON object", ErrRecord, id)
	}
	return raw, fileVersion(info), nil
}

// fileVersion keeps file versions apart from row revisions.
func fileVersion(info os.FileInfo) Version {
	key := store.VersionOf(info).Key()
	if key == "" {
		return ""
	}
	return Version("f" + key)
}

func legacyVersion(sessions, id string) (Version, bool) {
	root, err := os.OpenRoot(sessions)
	if err != nil {
		return "", false
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Join(id, legacyName))
	if err != nil {
		return "", false
	}
	return fileVersion(info), true
}

// ImportLegacy copies every session.json that has no record yet into dyna.db,
// one short transaction per session and no session lock, so it never holds
// up a session for long. A record written meanwhile wins. Damaged files are
// skipped and returned; the files themselves are never moved or deleted.
func ImportLegacy(sessions string) (imported int, damaged error) {
	ids, err := SafeIDs(sessions)
	if err != nil {
		return 0, err
	}
	db, err := records(sessions)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if checkID(id) != nil {
			continue
		}
		var exists int
		if db.QueryRow("SELECT 1 FROM session_records WHERE id = ?", id).Scan(&exists) == nil {
			continue
		}
		raw, _, err := legacyLoadRaw(sessions, id)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil {
			err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
				return writeRow(conn, id, raw, "ON CONFLICT(id) DO NOTHING")
			})
		}
		if err != nil {
			damaged = errors.Join(damaged, fmt.Errorf("import session %s: %w", id, err))
			continue
		}
		imported++
	}
	return imported, damaged
}

// TODO: legacy/shim remove later — open listings find session folders with no row (a session.json not imported yet) through this folder scan; remove legacyFolders and legacyScans with legacyLoadRaw once every store has run ImportLegacy.
var legacyScans sync.Map // sessions folder -> legacyScan

type legacyScan struct {
	key    string
	ids    []string
	unsafe error
}

// legacyFolders lists the session folders that have no row, and an ErrRecord
// error (unsafe) for symlinked entries, as Versions does. One scan reads
// every row and folder, so its result is kept until the sessions folder or
// the record revision changes; both are read before the scan, so a change
// during it makes the next call scan again.
func legacyFolders(db *sql.DB, ok bool, sessions string) (legacyScan, error) {
	info, err := os.Stat(sessions)
	if errors.Is(err, os.ErrNotExist) {
		return legacyScan{}, nil
	}
	if err != nil {
		return legacyScan{}, err
	}
	key := store.VersionOf(info).Key()
	if ok && key != "" {
		rev, err := revision(db)
		if err != nil {
			return legacyScan{}, err
		}
		key += "|" + rev
	}
	if value, hit := legacyScans.Load(sessions); hit && key != "" && value.(legacyScan).key == key {
		return value.(legacyScan), nil
	}
	rows := map[string]Version{}
	if ok {
		if err := readVersions(db, rows); err != nil {
			return legacyScan{}, err
		}
	}
	entries, err := os.ReadDir(sessions)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return legacyScan{}, err
	}
	scan := legacyScan{key: key}
	for _, e := range entries {
		id := e.Name()
		if e.Type()&os.ModeSymlink != 0 {
			scan.unsafe = errors.Join(scan.unsafe, fmt.Errorf("%w: session entry %q is a symlink", ErrRecord, id))
		} else if _, ok := rows[id]; !ok && e.IsDir() {
			scan.ids = append(scan.ids, id)
		}
	}
	if key != "" {
		legacyScans.Store(sessions, scan)
	}
	return scan, nil
}
