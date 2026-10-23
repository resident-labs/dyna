package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// The release check record lives in dyna.db (rule r1): one row with the last
// check time, the ETag, the cached release and the last error. It stays a
// cache, so a process that cannot reach the table still checks GitHub; it just
// cannot reuse a previous answer.
const checkSchema = `CREATE TABLE update_check (
 id INTEGER PRIMARY KEY CHECK(id = 1),
 last_checked INTEGER NOT NULL,
 etag TEXT NOT NULL,
 release TEXT NOT NULL CHECK(json_valid(release)),
 last_error TEXT NOT NULL
);`

// checkFile is the release check record from before dyna.db.
const checkFile = "update-check.json"

// checkState is the cached result of one release check.
type checkState struct {
	// The JSON names are the retained file's; only the import reads them.
	LastChecked time.Time `json:"lastChecked"`
	ETag        string    `json:"etag"`
	Release     Release   `json:"release"`
	LastError   string    `json:"lastError"`
}

// Migrate brings the release check table in dataDir's dyna.db to this version
// and imports the retained file. Only the engine calls it, at start
// (requirement 41).
func Migrate(dataDir string) error {
	_, err := checkDB(dataDir)
	return err
}

func checkDB(dataDir string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "update.check", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, checkSchema); err != nil {
			return err
		}
		return importCheckFile(ctx, conn, checkFilePath(dataDir))
	})
	return db, err
}

// checkFilePath is where the retained release check file lives.
func checkFilePath(dataDir string) string { return filepath.Join(dataDir, checkFile) }

// TODO: legacy/shim remove later — imports update-check.json once inside the
// update.check migration; remove when no install keeps a file from before
// dyna.db. The file stays on disk unchanged, so an older binary still reads
// its own cache and rewrites it.
func importCheckFile(ctx context.Context, conn *sql.Conn, file string) error {
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var st checkState
	// A cache that cannot be read or parsed is dropped, as the old reader did:
	// it ran with no cache and rewrote the file at its next check.
	if err != nil || json.Unmarshal(raw, &st) != nil {
		return nil
	}
	if st.LastChecked.IsZero() && st.ETag == "" && st.Release.TagName == "" && st.LastError == "" {
		return nil
	}
	return putCheckState(ctx, conn, st)
}

// loadState reads the cached check. Its caller treats an error as a cache
// miss, so a process that cannot open dyna.db still checks GitHub.
func (c Config) loadState() (checkState, error) {
	db, err := checkDB(c.DataDir)
	if err != nil {
		return checkState{}, err
	}
	var (
		st   checkState
		at   int64
		body string
	)
	err = db.QueryRowContext(context.Background(),
		"SELECT last_checked, etag, release, last_error FROM update_check WHERE id = 1").
		Scan(&at, &st.ETag, &body, &st.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return checkState{}, nil
	}
	if err != nil {
		return checkState{}, err
	}
	st.LastChecked = time.Unix(0, at).UTC()
	if err := json.Unmarshal([]byte(body), &st.Release); err != nil {
		return checkState{}, fmt.Errorf("read cached release: %w", err)
	}
	return st, nil
}

// saveState replaces the cached check. Its caller ignores an error: the
// record is a cache, and a failed write must not fail the update.
func (c Config) saveState(st checkState) error {
	db, err := checkDB(c.DataDir)
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return putCheckState(context.Background(), conn, st)
	})
}

func putCheckState(ctx context.Context, conn *sql.Conn, st checkState) error {
	body, err := json.Marshal(st.Release)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO update_check (id, last_checked, etag, release, last_error)
VALUES (1, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
 last_checked=excluded.last_checked, etag=excluded.etag,
 release=excluded.release, last_error=excluded.last_error`,
		st.LastChecked.UTC().UnixNano(), st.ETag, string(body), st.LastError)
	return err
}
