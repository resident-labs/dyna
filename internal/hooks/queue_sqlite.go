package hooks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// One row per pending event. The autoincrement id keeps the append order,
// across processes, and never reuses the id of a drained row.
const queueSchema = `CREATE TABLE hook_queue (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 body TEXT NOT NULL CHECK(json_valid(body))
)`

func (s Service) queueDB() (*sql.DB, error) {
	db, err := database.Open(s.dataDir())
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "hooks.queue", 1, func(conn *sql.Conn, old int) error {
		if old < 1 {
			_, err := conn.ExecContext(context.Background(), queueSchema)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// appendQueued inserts one event as the newest queue row.
func (s Service) appendQueued(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	db, err := s.queueDB()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "INSERT INTO hook_queue(body) VALUES(?)", string(body))
		return err
	})
}

// maxSpoolEvent bounds one legacy event file. A real event is a few hundred
// bytes, so a larger file is not an event.
const maxSpoolEvent = 1 << 20

// spoolFile is one legacy queue file: its name, its parsed sequence number
// (the order the old emitter appended in) and its size.
type spoolFile struct {
	name     string
	sequence uint64
	size     int64
}

// TODO: legacy/shim remove later — imports events a previous-release emitter
// left as files under hook-spool, on every drain, until no old emitter
// remains; each file is removed only after its row committed, so a crash
// replays the import instead of losing the event.
func (s Service) importSpoolFiles(ctx context.Context) error {
	dir := filepath.Join(s.dataDir(), "hook-spool")
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	// An old emitter holds write.lock while it appends. Hold it across the
	// listing and the imports so no file appears half-written.
	unlock, err := store.Lock(filepath.Join(dir, "write.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	files, err := spoolFiles(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := s.importSpoolFile(ctx, dir, f); err != nil {
			return err
		}
	}
	// The lock file and the emptied directory go best-effort: quarantined or
	// unrelated leftovers keep the directory, as they did before.
	_ = os.Remove(filepath.Join(dir, "write.lock"))
	_ = os.Remove(dir)
	return nil
}

// importSpoolFile imports one legacy file: a file this release refuses is
// quarantined beside the queue and blocks nothing, as the old drain did.
func (s Service) importSpoolFile(ctx context.Context, dir string, f spoolFile) error {
	path := filepath.Join(dir, f.name)
	raw, err := readSpoolFile(path, f)
	if err != nil {
		return quarantineSpoolFile(dir, f.name, err)
	}
	e, err := decodeSpoolEvent(raw)
	if err != nil {
		return quarantineSpoolFile(dir, f.name, err)
	}
	if err := s.appendQueued(ctx, e); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove imported hook spool file %s: %w", path, err)
	}
	return nil
}

// spoolFiles lists the legacy queue files in append order, skipping names
// the old emitter never wrote.
func spoolFiles(dir string) ([]spoolFile, error) {
	entries, err := os.ReadDir(dir)
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
	return files, nil
}

// errRetainedSnapshot marks a legacy record whose snapshot execution tree an
// old emitter owns; it needs the approved old-emitter drain.
var errRetainedSnapshot = errors.New("retained snapshot requires the approved old-emitter drain")

// decodeSpoolEvent decodes one legacy event file. Every case-insensitive
// snapshot marker must be a literal false, checked one by one as the old
// startup validator did: a true marker is a retained snapshot, and a marker
// that is not a boolean is not an event this release can interpret.
func decodeSpoolEvent(raw []byte) (Event, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Event{}, err
	}
	if fields == nil {
		return Event{}, errors.New("event is not an object")
	}
	for key, marker := range fields {
		if !strings.EqualFold(key, "snapshot") {
			continue
		}
		switch string(bytes.TrimSpace(marker)) {
		case "false":
		case "true":
			return Event{}, errRetainedSnapshot
		default:
			return Event{}, fmt.Errorf("snapshot marker is not a boolean: %s", marker)
		}
	}
	var e Event
	if err := json.Unmarshal(raw, &e); err != nil {
		return Event{}, err
	}
	return e, nil
}

// quarantineSpoolFile moves a file the importer refuses beside the queue.
func quarantineSpoolFile(dir, name string, cause error) error {
	if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, name+".bad")); err != nil {
		return err
	}
	log.Printf("hook spool: quarantined %q: %v", name, cause)
	return nil
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
