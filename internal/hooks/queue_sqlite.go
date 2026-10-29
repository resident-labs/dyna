package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

// One row per pending event. The autoincrement id keeps the append order,
// across processes, and never reuses the id of a drained row.
const queueSchema = `CREATE TABLE hook_queue (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 body TEXT NOT NULL CHECK(json_valid(body))
)`

// maxSpoolEvent bounds one imported legacy event file. A real event is a few
// hundred bytes, so a larger file is not an event.
const maxSpoolEvent = 1 << 20

func (s Service) queueDB() (*sql.DB, error) {
	db, err := database.Open(s.dataDir())
	if err != nil {
		return nil, err
	}
	spool := filepath.Join(s.dataDir(), "hook-spool")
	// A previous-release emitter holds write.lock while it appends. Hold it
	// across the one-time import so no event file appears between the
	// listing and the commit. It is taken only while the retired directory
	// exists, so fresh installs and steady state pay nothing.
	var unlock func()
	if info, statErr := os.Stat(spool); statErr == nil && info.IsDir() {
		if unlock, err = store.Lock(filepath.Join(spool, "write.lock")); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	var imported []string
	err = database.Migrate(db, "hooks.queue", 1, func(conn *sql.Conn, old int) error {
		if old >= 1 {
			return nil
		}
		if _, err := conn.ExecContext(context.Background(), queueSchema); err != nil {
			return err
		}
		imported, err = s.importSpool(conn, spool)
		return err
	})
	if unlock != nil {
		unlock()
	}
	if err != nil {
		return nil, err
	}
	// Files go only after their rows committed: a crash in between leaves the
	// imported files behind, never half-imported rows.
	for _, path := range imported {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove imported hook spool file %s: %w", path, err)
		}
	}
	if len(imported) > 0 {
		// The lock file and the emptied directory go best-effort: unrelated
		// leftovers keep the directory, as they did before.
		_ = os.Remove(filepath.Join(spool, "write.lock"))
		_ = os.Remove(spool)
	}
	return db, nil
}

// spoolFile is one legacy queue file: its name, its parsed sequence number
// (the order the old emitter appended in) and its size.
type spoolFile struct {
	name     string
	sequence uint64
	size     int64
}

// TODO: legacy/shim remove later — imports the previous release's file spool
// once, in sequence order, and returns the files it imported; the caller
// removes them after the migration commits. A file that is not a bounded,
// valid event fails the import, and so engine start, until the operator
// decides, as the old startup validator did. A decoded snapshot marker is
// refused the same way: such an event needs the approved old-emitter drain.
func (s Service) importSpool(conn *sql.Conn, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []spoolFile
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".json") {
			continue
		}
		sequence, err := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, spoolFile{name: name, sequence: sequence, size: info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].sequence < files[j].sequence })
	ctx := context.Background()
	var imported []string
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		raw, err := readSpoolFile(path, f)
		if err != nil {
			return nil, fmt.Errorf("import hook spool %s: %w", path, err)
		}
		var fields map[string]json.RawMessage
		var e Event
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("import hook spool %s: event is not an object", path)
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("import hook spool %s: %w", path, err)
		}
		if e.Snapshot {
			return nil, fmt.Errorf("import hook spool %s: retained snapshot requires the approved old-emitter drain before startup", path)
		}
		body, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("import hook spool %s: %w", path, err)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO hook_queue(body) VALUES(?)", string(body)); err != nil {
			return nil, fmt.Errorf("import hook spool %s: %w", path, err)
		}
		imported = append(imported, path)
	}
	return imported, nil
}

// readSpoolFile reads one bounded legacy event file. The size check before
// the read bounds the case where the file grows while the importer holds the
// old emitter's lock.
func readSpoolFile(path string, f spoolFile) ([]byte, error) {
	if f.size > maxSpoolEvent {
		return nil, fmt.Errorf("%d bytes exceeds the %d-byte event bound", f.size, maxSpoolEvent)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxSpoolEvent {
		return nil, fmt.Errorf("%d bytes exceeds the %d-byte event bound", len(raw), maxSpoolEvent)
	}
	return raw, nil
}
