package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"syscall"

	"dyna-agent/internal/database"
	appstate "dyna-agent/internal/state"
)

func Root(dataDir string) string  { return filepath.Join(dataDir, "prs") }
func LockPath(root string) string { return filepath.Join(root, "state.lock") }

// ReviewInputsDir holds the reviewer input packs of the PR with key.
func ReviewInputsDir(root, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(root, "review-inputs", hex.EncodeToString(sum[:])[:20])
}

const schema = `CREATE TABLE IF NOT EXISTS pr_records (
 key TEXT PRIMARY KEY, room TEXT NOT NULL, repo TEXT NOT NULL, number INTEGER NOT NULL,
 closed INTEGER NOT NULL, rev INTEGER NOT NULL, body TEXT NOT NULL CHECK(json_valid(body))
);
CREATE INDEX IF NOT EXISTS pr_records_room ON pr_records(room);
CREATE TABLE IF NOT EXISTS pr_state (
 id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL, body TEXT NOT NULL CHECK(json_valid(body))
);`

// The engine uses one PR store. Keeping only its latest snapshot bounds memory
// and shares parsing across workers, API handlers and short-lived services.
// Each row records the revision that last wrote it, so a refresh decodes only
// the rows newer than the snapshot.
var snapshot struct {
	sync.Mutex
	root     string
	revision int64
	index    []byte // the stored index-level JSON at revision
	state    State
}

// Migrate brings the PR state tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := open(Root(dataDir))
	return err
}

// open returns the shared database in root's data folder with the PR tables ready.
// The first open imports the JSON store in root, which stays on disk unchanged.
func open(root string) (*sql.DB, error) {
	db, err := database.Open(filepath.Dir(root))
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "pr.state", 1, func(conn *sql.Conn, _ int) error {
		if _, err := conn.ExecContext(context.Background(), schema); err != nil {
			return err
		}
		var owned bool
		if err := conn.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM pr_state) OR EXISTS(SELECT 1 FROM pr_records)`).Scan(&owned); err != nil {
			return err
		}
		if owned {
			return nil
		}
		st, found, err := readLegacy(root)
		if err != nil || !found {
			return err
		}
		return importLegacyRows(conn, st)
	})
	if err != nil {
		return db, err
	}
	// An additive index has its own key and leaves the PR table version unchanged.
	err = database.Migrate(db, "pr.state.revision-index", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), "CREATE INDEX IF NOT EXISTS pr_records_revision ON pr_records(rev, key)")
		return err
	})
	if err != nil {
		return db, err
	}
	err = database.Migrate(db, "pr.closed_at", 1, migrateClosedAt)
	if err != nil {
		return db, err
	}
	err = database.Migrate(db, "pr.landing_history", 1, migrateLanding)
	if err != nil {
		return db, err
	}
	err = database.Migrate(db, "pr.policy_retirement", 1, migratePolicy)
	if err != nil {
		return db, err
	}
	err = database.Migrate(db, "pr.policy_defaults", 1, migratePolicyDefaults)
	if err != nil {
		return db, err
	}
	err = database.Migrate(db, "pr.step_budget_verdicts", 1, migrateWallBudget)
	if err != nil {
		return db, err
	}
	err = database.Migrate(db, "pr.quota_scopes", 1, migrateQuota)
	if err != nil {
		return db, err
	}
	err = database.Migrate(db, "pr.record_parts", 1, migrateRecordParts)
	return db, err
}

// Revision counts committed changes; scanners compare it to skip unchanged stores.
func Revision(root string) (string, error) {
	db, err := open(root)
	if err != nil {
		return "", err
	}
	var revision int64
	err = db.QueryRow("SELECT revision FROM pr_state WHERE id=1").Scan(&revision)
	if err == sql.ErrNoRows {
		return "missing", nil
	}
	return strconv.FormatInt(revision, 10), err
}

// KeysChangedSince lists the records written after revision since. Records
// deleted since then leave no row; callers find those themselves.
func KeysChangedSince(root, since string) ([]string, error) {
	db, err := open(root)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT key FROM pr_records WHERE rev > ?", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// Read returns an independent mutable copy of the current PR snapshot.
func Read(root string) (State, error) {
	value, err := ReadSnapshot(root)
	if err != nil {
		return State{}, err
	}
	return value.Copy(), nil
}

// ReadSnapshot waits for the PR fence and retains an immutable committed view.
func ReadSnapshot(root string) (Snapshot, error) {
	f, err := os.Open(LockPath(root))
	if err != nil && !os.IsNotExist(err) {
		return Snapshot{}, err
	}
	if err == nil {
		defer f.Close()
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
			return Snapshot{}, err
		}
		defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
	return readSnapshot(root)
}

// ReadLocked reads while the caller owns the store's exclusive lock, before
// it writes. Every PR write commits under that lock, so a read transaction
// sees each committed one without taking the writer.
func ReadLocked(root string) (State, error) { return read(root) }

var copyForRead = cloneState

// read refreshes the shared snapshot in one read transaction, so the index
// and every row have the same revision.
func read(root string) (State, error) {
	value, err := readSnapshot(root)
	if err != nil {
		return State{}, err
	}
	return value.Copy(), nil
}

// readSnapshot retains the data and revision from the same read transaction.
func readSnapshot(root string) (Snapshot, error) {
	db, err := open(root)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Lock()
	defer snapshot.Unlock()
	if err = refreshFrom(appstate.For(db), root); err != nil {
		return Snapshot{}, err
	}
	revision := "missing"
	if snapshot.index != nil {
		revision = strconv.FormatInt(snapshot.revision, 10)
	}
	return Snapshot{state: snapshot.state, revision: revision}, nil
}

// refreshFrom copies the changed rows in one short read transaction, then
// decodes them after it ends. A long read would hold back WAL checkpoints
// and grow the WAL. The caller holds snapshot.
func refreshFrom(w *appstate.Writer, root string) error {
	var f fetched
	err := w.Read(context.Background(), func(r appstate.Reader) error {
		var err error
		f, err = fetch(r, root)
		return err
	})
	if err != nil {
		return err
	}
	return f.install()
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// storedRow is a pr_records row copied out of the read transaction.
type storedRow struct {
	key   string
	rev   int64
	body  []byte // nil when the cached record is current
	parts storedParts
}

// fetched is what one read transaction returned.
type fetched struct {
	missing, unchanged bool
	revision           int64
	index              []byte
	rows               []storedRow
}

// unmarshalRecord is replaced by tests.
var unmarshalRecord = func(data []byte, record *diskRecord) error { return json.Unmarshal(data, record) }

// fetch copies the store's revision, its index and the rows newer than the
// cached snapshot. It decodes nothing. The caller holds snapshot.
func fetch(q querier, root string) (fetched, error) {
	ctx := context.Background()
	if snapshot.root != root {
		snapshot.root, snapshot.revision, snapshot.index, snapshot.state = root, 0, nil, newState()
	}
	rows, err := q.QueryContext(ctx, "SELECT revision, body FROM pr_state WHERE id=1")
	if err != nil {
		return fetched{}, err
	}
	var f fetched
	found := false
	for rows.Next() {
		found = true
		if err = rows.Scan(&f.revision, &f.index); err != nil {
			rows.Close()
			return fetched{}, err
		}
	}
	if err = rows.Close(); err != nil {
		return fetched{}, err
	}
	if !found {
		return fetched{missing: true}, nil
	}
	if snapshot.index != nil && f.revision == snapshot.revision {
		return fetched{unchanged: true}, nil
	}
	rows, err = q.QueryContext(ctx, "SELECT key, rev, body FROM pr_records")
	if err != nil {
		return fetched{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var row storedRow
		var body sql.RawBytes
		if err = rows.Scan(&row.key, &row.rev, &body); err != nil {
			return fetched{}, err
		}
		if cached := snapshot.state.Records[row.key]; cached == nil || row.rev > snapshot.revision {
			row.body = append([]byte{}, body...)
		}
		f.rows = append(f.rows, row)
	}
	if err := rows.Err(); err != nil {
		return fetched{}, err
	}
	return f, fetchParts(ctx, q, &f)
}

// fetchParts copies the cold parts of the rows that carry a body. The lowest
// revision among those rows bounds the query, so a refresh reads only the
// parts of changed records.
func fetchParts(ctx context.Context, q querier, f *fetched) error {
	byKey := map[string]*storedRow{}
	var lowest int64
	for i := range f.rows {
		row := &f.rows[i]
		if row.body == nil {
			continue
		}
		byKey[row.key] = row
		if len(byKey) == 1 || row.rev < lowest {
			lowest = row.rev
		}
	}
	if len(byKey) == 0 {
		return nil
	}
	rows, err := q.QueryContext(ctx, "SELECT key, part, body FROM pr_record_parts WHERE key IN (SELECT key FROM pr_records WHERE rev >= ?)", lowest)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, part string
		var body sql.RawBytes
		if err := rows.Scan(&key, &part, &body); err != nil {
			return err
		}
		row := byKey[key]
		if row == nil {
			continue
		}
		if row.parts == nil {
			row.parts = storedParts{}
		}
		row.parts[partName(part)] = append([]byte{}, body...)
	}
	return rows.Err()
}

// install brings the cached snapshot up to the fetched revision. The caller holds snapshot.
func (f fetched) install() error {
	if f.missing {
		snapshot.revision, snapshot.index, snapshot.state = 0, nil, newState()
		return nil
	}
	if f.unchanged {
		return nil
	}
	revision, index := f.revision, f.index
	st := newState()
	if err := json.Unmarshal(index, &st); err != nil {
		return fmt.Errorf("invalid PR state: %w", err)
	}
	st.Records, st.Responses = map[string]*Record{}, map[string]map[string]CachedResponse{}
	reused := map[string]*Record{}
	for _, row := range f.rows {
		key := row.key
		if row.body == nil {
			cached := snapshot.state.Records[key]
			// Normalization updates derived quota fields. Keep the
			// record in an already retained snapshot unchanged.
			copy := *cached
			st.Records[key] = &copy
			reused[key] = cached
			if responses := snapshot.state.Responses[key]; responses != nil {
				st.Responses[key] = responses
			}
			continue
		}
		record, err := joinStored(row.body, row.parts)
		if err != nil {
			return fmt.Errorf("invalid PR record %s: %w", key, err)
		}
		st.Records[key] = record.Record
		if record.Responses != nil {
			st.Responses[key] = record.Responses
		}
	}
	normalizeState(&st)
	// Keep stable identities for rows whose stored and derived fields did not
	// change. The shallow copies above protect retained snapshots while normalizing.
	for key, old := range reused {
		current := st.Records[key]
		if current.ClosedAt == old.ClosedAt && reflect.DeepEqual(current.RateLimit, old.RateLimit) {
			st.Records[key] = old
		}
	}
	snapshot.revision, snapshot.index, snapshot.state = revision, index, st
	return nil
}

func normalizeState(st *State) {
	if st.RateLimits == nil {
		st.RateLimits = map[string]RateLimit{}
	}
	// A row whose body holds no record, such as {"record":null}, is left
	// out, so no caller meets a nil record.
	for key, r := range st.Records {
		if r == nil {
			delete(st.Records, key)
			delete(st.Responses, key)
			continue
		}
		// A client renders only a known local CI state, so a value written by an
		// older or a broken writer never reaches a view as prose.
		if !r.LocalCIConfig.known() {
			r.LocalCIConfig = LocalCIConfigNone
		}
	}
	st.RefreshQuotaDisplay()
	if st.Records == nil {
		st.Records = map[string]*Record{}
	}
	if st.Reviewers == nil {
		st.Reviewers = map[string]Reviewer{}
	}
	if st.NoiseRules == nil {
		st.NoiseRules = map[string]NoiseRule{}
	}
}
