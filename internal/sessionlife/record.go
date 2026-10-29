package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// The session record is the metadata of one session. Every read and write of
// it goes through this file. It is a row of session_records in dyna.db, the
// database in the data folder above sessions. The session's folder still
// holds its turns, transcripts and locks.
//
// The columns are what listings filter and sort on; meta is the hot record.
// Content-addressed blobs hold the two large instruction fields. Readers join
// those fields back into the complete record. revision grows with every write in the table, so a reader can tell whether
// its cached copy is current. session_records_closed covers the listing
// queries (every version, or open ones only), so they never read the wide
// meta rows. session_records_effective_name covers a lookup by address or
// name, which must not read every record either. dir, service, meta_id and
// meta_digest are the ownership projection Owners reads, so worktree
// ownership inference never decodes every record either.
const recordSchema = `CREATE TABLE IF NOT EXISTS session_records (
  id TEXT PRIMARY KEY,
  revision INTEGER NOT NULL,
  room_id TEXT NOT NULL,
  room TEXT NOT NULL,
  name TEXT NOT NULL,
  status TEXT NOT NULL,
  closed INTEGER NOT NULL,
  parent TEXT NOT NULL,
  created_ns INTEGER NOT NULL,
  activity_ms INTEGER NOT NULL,
  history_only INTEGER NOT NULL DEFAULT 0,
  meta TEXT NOT NULL,
  dir TEXT NOT NULL DEFAULT '',
  service INTEGER NOT NULL DEFAULT 0,
  meta_id TEXT NOT NULL DEFAULT '',
  meta_digest TEXT NOT NULL DEFAULT '',
  instructions_hash TEXT NOT NULL DEFAULT '',
  instructions_known_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS session_records_room ON session_records(room_id, closed);
CREATE UNIQUE INDEX IF NOT EXISTS session_records_revision ON session_records(revision);
CREATE INDEX IF NOT EXISTS session_records_closed ON session_records(closed, id, revision);
CREATE INDEX IF NOT EXISTS session_records_effective_name ON session_records(` + effectiveName + `, room);
` + instructionBlobSchema

// recordLimit bounds one record; real records stay under 1 MB.
const recordLimit = 4 << 20

// ErrRecord marks a record that exists but is unsafe or not a JSON object.
var ErrRecord = errors.New("session record is unsafe or invalid")

func checkID(id string) error {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return fmt.Errorf("%w: bad session ID %q", ErrRecord, id)
	}
	return nil
}

// Migrate brings the session record and steering tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	sessions := filepath.Join(dataDir, "sessions")
	db, err := records(sessions)
	if err != nil {
		return err
	}
	if err := migrateSessionImport(db, sessions); err != nil {
		return err
	}
	if err := migrateService(db); err != nil {
		return err
	}
	if err := migrateHistory(db); err != nil {
		return err
	}
	if err := migrateLifecycle(db); err != nil {
		return err
	}
	if err := migrateNames(db); err != nil {
		return err
	}
	_, err = steeringDB(sessions)
	return err
}

const recordSchemaVersion = 5

// records opens dyna.db for the sessions folder and prepares the table, for
// writes. It takes the database writer when the table is new.
func records(sessions string) (*sql.DB, error) {
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		return nil, err
	}
	// Version 2 adds session_records_closed, version 3
	// session_records_effective_name, version 4 the ownership columns, and
	// version 5 the instruction blob import; the schema is idempotent.
	err = database.Migrate(db, "sessions.records", recordSchemaVersion, func(conn *sql.Conn, old int) error {
		if _, err := conn.ExecContext(context.Background(), recordSchema); err != nil {
			return err
		}
		if old > 0 && old < 4 {
			if err := backfillOwnerColumns(context.Background(), conn); err != nil {
				return err
			}
		}
		if old < 5 {
			return migrateInstructionBlobs(context.Background(), conn)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := prepareHistory(db); err != nil {
		return nil, err
	}
	tables.Store(db, true)
	return db, nil
}

// Prepare opens the record store and creates its table if needed. A store
// that reads or writes records inside its own dyna.db transaction calls it
// first: creating the table needs the writer, which that transaction holds.
func Prepare(sessions string) error {
	_, err := records(sessions)
	return err
}

// tables remembers the handles whose record table exists.
var tables sync.Map // *sql.DB -> true

var recordSchemaRetry = func(err error) bool { return database.IsUpdatingSchema(err, "sessions.records") }
var recordSchemaPause = func(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// readable opens dyna.db for reads. It never creates the table, so a read
// inside another store's transaction never waits for the writer; until the
// first write there are no rows.
func readable(sessions string) (*sql.DB, bool, error) {
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		return nil, false, err
	}
	if _, ok := tables.Load(db); ok {
		return db, true, nil
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'session_records'").Scan(&n); err != nil {
		return nil, false, err
	}
	if n == 0 {
		return db, false, nil
	}
	if !database.OwnsSchema() {
		// An installed client can start before the engine migrates a new schema.
		// Migrate only checks the version outside the engine, so its migration
		// callback is unreachable here. Wait for the engine, never migrate or
		// query a table whose schema is not ready. The attempt bound and context
		// deadline keep an unavailable engine from blocking a reader forever.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for attempt := 0; ; attempt++ {
			err = database.Migrate(db, "sessions.records", recordSchemaVersion, nil)
			if err == nil {
				break
			}
			if !recordSchemaRetry(err) || attempt == 120 {
				return nil, false, err
			}
			if waitErr := recordSchemaPause(ctx); waitErr != nil {
				return nil, false, errors.Join(err, waitErr)
			}
		}
	}
	tables.Store(db, true)
	return db, true, nil
}

// querier is a database handle or a connection inside a transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Load decodes session id's record into v. A missing record wraps
// os.ErrNotExist.
func Load(sessions, id string, v any) error {
	raw, _, err := LoadRaw(sessions, id)
	if err != nil {
		return err
	}
	return decode(id, raw, v)
}

func decode(id string, raw []byte, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: session %s: %v", ErrRecord, id, err)
	}
	return nil
}

// Version identifies one write of a record. Compare versions with ==; an
// empty version is never current, because its writes cannot be told apart.
type Version string

// LoadRaw returns session id's record and its version.
func LoadRaw(sessions, id string) ([]byte, Version, error) {
	db, ok, err := readable(sessions)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		if err := checkID(id); err != nil {
			return nil, "", err
		}
		return nil, "", os.ErrNotExist
	}
	return loadRaw(db, sessions, id)
}

func loadRaw(q querier, sessions, id string) ([]byte, Version, error) {
	if err := checkID(id); err != nil {
		return nil, "", err
	}
	raw, version, err := loadStored(context.Background(), q, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", os.ErrNotExist
	}
	return raw, version, err
}

// LoadStored decodes a canonical record from the caller's database snapshot.
// It never imports or reads a legacy file; a missing row returns sql.ErrNoRows.
func LoadStored(ctx context.Context, q state.Reader, id string, v any) error {
	_, err := LoadStoredVersion(ctx, q, id, v)
	return err
}

// LoadStoredVersion is LoadStored that also returns the version it decoded.
func LoadStoredVersion(ctx context.Context, q state.Reader, id string, v any) (Version, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	raw, version, err := loadStored(ctx, q, id)
	if err != nil {
		return "", err
	}
	return version, decode(id, raw, v)
}

// StoredVersion reads only the version of a canonical record, so a caller
// that caches decoded records can skip the load and decode while it holds.
func StoredVersion(ctx context.Context, q state.Reader, id string) (Version, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	var revision int64
	if err := q.QueryRowContext(ctx, "SELECT revision FROM session_records WHERE id = ?", id).Scan(&revision); err != nil {
		return "", err
	}
	return rowVersion(revision), nil
}

func loadStored(ctx context.Context, q querier, id string) ([]byte, Version, error) {
	var meta string
	var revision int64
	instructions, err := scanInstructionRead(q.QueryRowContext(ctx, "SELECT meta,revision,"+instructionReadProjection+" FROM session_records WHERE id = ?", id), &meta, &revision)
	if err != nil {
		return nil, "", err
	}
	if !isObject([]byte(meta)) {
		return nil, "", fmt.Errorf("%w: session %s record is not a bounded JSON object", ErrRecord, id)
	}
	raw, err := instructions.join([]byte(meta))
	if err != nil {
		return nil, "", fmt.Errorf("%w: session %s: %v", ErrRecord, id, err)
	}
	if len(raw) > recordLimit {
		return nil, "", fmt.Errorf("%w: session %s record exceeds %d bytes", ErrRecord, id, recordLimit)
	}
	return raw, rowVersion(revision), nil
}

func rowVersion(revision int64) Version { return Version("r" + strconv.FormatInt(revision, 10)) }

// RecordVersion reports the version of session id's record without reading
// it, so callers can cache decoded records.
func RecordVersion(sessions, id string) (Version, bool) {
	if checkID(id) != nil {
		return "", false
	}
	db, ok, err := readable(sessions)
	if err != nil {
		return "", false
	}
	if !ok {
		return "", false
	}
	var revision int64
	err = db.QueryRow("SELECT revision FROM session_records WHERE id = ?", id).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		return "", false
	}
	return rowVersion(revision), true
}

// Topic is the change bus topic of a session write. The engine turns it into
// a members wake for the session in Key; it never reaches clients, which get
// members events from the room watch.
const Topic = "sessions"

// Changed is the event of a write to session id.
func Changed(id string) hub.Event { return hub.Event{Topic: Topic, Key: id} }

// Save replaces session id's record with v. Never call it inside an open
// dyna.db transaction (the writer waits on it); use SaveTx there.
func Save(sessions, id string, v any) error {
	return SaveContext(context.Background(), sessions, id, v)
}

// SaveContext is Save for a call that carries a request ID: the ID is
// recorded in the same transaction as the record, so a resend after a crash
// does not write it twice (requirement 9).
func SaveContext(ctx context.Context, sessions, id string, v any) error {
	db, err := records(sessions)
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, Changed(id), func(conn *sql.Conn) error { return SaveTx(conn, id, v) })
}

// SaveIf replaces session id's record with v in one write transaction, only
// while still accepts the record that transaction reads; otherwise it
// returns still's error and writes nothing.
func SaveIf(sessions, id string, v any, still func(raw []byte) error) error {
	db, err := records(sessions)
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), Changed(id), func(conn *sql.Conn) error {
		raw, _, err := loadRaw(conn, sessions, id)
		if err != nil {
			return err
		}
		if err := still(raw); err != nil {
			return err
		}
		return SaveTx(conn, id, v)
	})
}

// SaveTx is Save inside the caller's dyna.db write transaction on conn.
func SaveTx(conn *sql.Conn, id string, v any) error {
	if err := checkID(id); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if !isObject(raw) || len(raw) > recordLimit {
		return fmt.Errorf("%w: session %s record is not a JSON object under %d bytes", ErrRecord, id, recordLimit)
	}
	return writeRow(conn, id, raw, "ON CONFLICT(id) DO UPDATE SET revision=excluded.revision, room_id=excluded.room_id, room=excluded.room, name=excluded.name, status=excluded.status, closed=excluded.closed, parent=excluded.parent, created_ns=excluded.created_ns, activity_ms=excluded.activity_ms, history_only=excluded.history_only, meta=excluded.meta, dir=excluded.dir, service=excluded.service, meta_id=excluded.meta_id, meta_digest=excluded.meta_digest, instructions_hash=excluded.instructions_hash, instructions_known_hash=excluded.instructions_known_hash")
}

// recordColumns are the record fields copied into columns.
type recordColumns struct {
	Snapshot
	ID            string    `json:"id"`
	RoomID        string    `json:"roomId"`
	Room          string    `json:"room"`
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	ParentSession string    `json:"parentSession"`
	Created       time.Time `json:"created"`
	LastActivity  time.Time `json:"lastActivity"`
	Dir           string    `json:"dir"`
	Service       bool      `json:"service"`
}

func writeRow(conn *sql.Conn, id string, raw []byte, conflict string) error {
	var c recordColumns
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("%w: session %s: %v", ErrRecord, id, err)
	}
	closed := 0
	if c.IsClosed() {
		closed = 1
	}
	created := int64(0)
	if !c.Created.IsZero() {
		created = c.Created.UnixNano()
	}
	service := 0
	if c.Service {
		service = 1
	}
	instructions, err := splitInstructions(raw)
	if err != nil {
		return fmt.Errorf("%w: session %s: %v", ErrRecord, id, err)
	}
	previous, err := readInstructionHashes(conn, id)
	if err != nil {
		return err
	}
	if err := instructions.store(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(context.Background(), `INSERT INTO session_records(id, revision, room_id, room, name, status, closed, parent, created_ns, activity_ms, history_only, meta, dir, service, meta_id, meta_digest, instructions_hash, instructions_known_hash)
VALUES (?, (SELECT COALESCE(MAX(revision), 0) + 1 FROM session_records), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) `+conflict,
		id, c.RoomID, c.Room, c.Name, c.Status, closed, c.ParentSession, created, c.LastActivity.UnixMilli(), c.HistoryOnly, string(instructions.hot), c.Dir, service, c.ID, ownerDigest(raw), instructions.hashes[0], instructions.hashes[1])
	if err != nil {
		return err
	}
	return deleteUnreferencedInstructionBlobs(conn, previous, instructions.hashes)
}

func isObject(raw []byte) bool {
	if len(raw) > recordLimit || !json.Valid(raw) {
		return false
	}
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return c == '{'
	}
	return false
}

// AfterDelete notifies the metadata cache owner after a record deletion
// commits. subagent registers it at package initialization.
var AfterDelete = func(sessions, id string) {}

// Delete removes session id's record, its copied steering, and its turn and
// queue rows, for a session whose start failed before anything else used
// it, or one that retention removes.
func Delete(sessions, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	db, err := records(sessions)
	if err != nil {
		return err
	}
	if _, err := steeringDB(sessions); err != nil {
		return err
	}
	if err := state.For(db).Write(context.Background(), Changed(id), func(conn *sql.Conn) error {
		previous, err := readInstructionHashes(conn, id)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(context.Background(), "DELETE FROM session_records WHERE id = ?", id); err != nil {
			return err
		}
		if err := deleteUnreferencedInstructionBlobs(conn, previous, [2]string{}); err != nil {
			return err
		}
		if err := deleteTurnsAndQueue(conn, id); err != nil {
			return err
		}
		if err := deletePromptReferences(conn, id); err != nil {
			return err
		}
		if err := countDelete(conn); err != nil {
			return err
		}
		return deleteSteering(conn, id)
	}); err != nil {
		return err
	}
	AfterDelete(sessions, id)
	return nil
}

// deletePromptReferences removes only blobs owned by the deleted session
// that no other session references. Both deletes share the session transaction.
func deletePromptReferences(conn *sql.Conn, id string) error {
	ok, err := tableExists(conn, "prompt_refs")
	if err != nil || !ok {
		return err
	}
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `DELETE FROM prompt_blobs
WHERE hash IN (SELECT hash FROM prompt_refs WHERE session = ?)
AND NOT EXISTS (SELECT 1 FROM prompt_refs WHERE prompt_refs.hash = prompt_blobs.hash AND session != ?)`, id, id); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "DELETE FROM prompt_refs WHERE session = ?", id)
	return err
}

// turnsMirrorKey is the stamp key under which every turn copy of session id
// lies.
func turnsMirrorKey(id string) string { return "turns/" + id }

// TurnMirrorKey names the stamp of turn n's rollback copy of session id.
func TurnMirrorKey(id string, n int) string { return turnsMirrorKey(id) + "/" + strconv.Itoa(n) }

// QueueMirrorKey names the stamp of session id's queue rollback copy.
func QueueMirrorKey(id string) string { return "queues/" + id }

// deleteTurnsAndQueue deletes session id's rows in the turn and queue
// tables, and the stamps of their rollback copies. subagent owns these
// tables; a table that does not exist yet holds no rows.
func deleteTurnsAndQueue(conn *sql.Conn, id string) error {
	ctx := context.Background()
	for _, table := range []string{"session_turns", "session_queues", "post_deliveries"} {
		ok, err := tableExists(conn, table)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		key := "session"
		if table == "post_deliveries" {
			// A delivery row names its recipient in session_id. Rows keyed by an
			// address stay: they were never bound to this session.
			key = "session_id"
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+key+" = ?", id); err != nil {
			return err
		}
	}
	ok, err := tableExists(conn, "state_mirrors")
	if err != nil || !ok {
		return err
	}
	if err := state.ForgetMirrors(ctx, conn, turnsMirrorKey(id)); err != nil {
		return err
	}
	return state.ForgetMirrors(ctx, conn, QueueMirrorKey(id))
}

// TurnStatus returns the status in turn n's row of session id, from
// subagent's turn table, for readers that cannot import subagent. ok is
// false when the turn has no row yet: only an earlier release wrote it.
func TurnStatus(sessions, id string, n int) (status string, ok bool, err error) {
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		return "", false, err
	}
	var raw []byte
	err = state.For(db).Read(context.Background(), func(r state.Reader) error {
		var tables int
		if err := r.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'session_turns'").Scan(&tables); err != nil || tables == 0 {
			return err
		}
		err := r.QueryRowContext(context.Background(), "SELECT turn FROM session_turns WHERE session = ? AND n = ?", id, n).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil || raw == nil {
		return "", false, err
	}
	var turn struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &turn); err != nil {
		return "", false, fmt.Errorf("session %s turn %d: %w", id, n, err)
	}
	return turn.Status, true, nil
}

func tableExists(conn *sql.Conn, name string) (bool, error) {
	var n int
	err := conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?", name).Scan(&n)
	return n > 0, err
}

// DeletesKey counts deletes of session rows in dyna.db's meta table. A
// delete can free the highest revision of the session, turn or queue rows,
// so the next write would take it again and MAX(revision) would not move. A watcher reads this counter with
// the revisions (subagent's MembersRevision), from any process.
const DeletesKey = "sessions.deletes"

// countDelete counts one delete, in the delete's own transaction.
func countDelete(conn *sql.Conn) error {
	_, err := conn.ExecContext(context.Background(), `INSERT INTO meta(key, value) VALUES (?, '1')
ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1`, DeletesKey)
	return err
}

// IDs lists canonical session records in name order, including history.
func IDs(sessions string) ([]string, error) {
	versions, err := Versions(sessions)
	if versions == nil {
		return nil, err
	}
	out := make([]string, 0, len(versions))
	for id := range versions {
		out = append(out, id)
	}
	sort.Strings(out)
	if len(out) == 0 {
		out = nil
	}
	return out, err
}

// Versions is IDs with each record's version, read in one query, so a
// listing can check its cached copies without a query per session.
func Versions(sessions string) (map[string]Version, error) {
	return versions(sessions, versionsQuery)
}

// OpenVersions is Versions for open sessions only, including history. It
// reads session_records_closed without touching closed rows or session folders.
func OpenVersions(sessions string) (map[string]Version, error) {
	return versions(sessions, openQuery)
}

func versions(sessions, query string) (map[string]Version, error) {
	db, ok, err := readable(sessions)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Version)
	if ok {
		if err := scanVersions(db, query, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// The listing queries read only session_records_closed (see recordSchema).
const (
	versionsQuery = "SELECT id, revision FROM session_records"
	openQuery     = "SELECT id, revision FROM session_records WHERE closed = 0"
)

func scanVersions(db *sql.DB, query string, out map[string]Version) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var revision int64
		if err := rows.Scan(&id, &revision); err != nil {
			rows.Close()
			return err
		}
		out[id] = rowVersion(revision)
	}
	return errors.Join(rows.Err(), rows.Close())
}

// Each calls fn with every canonical session record in name order,
// including history. With open, the query leaves out closed records. A
// damaged record reaches fn with ErrRecord; fn decides whether to stop.
// The cursor closes before callbacks run, so callbacks can write records.
func Each(sessions string, open bool, fn func(id string, raw []byte, err error) error) error {
	db, ok, err := readable(sessions)
	if err != nil || !ok {
		return err
	}
	query := "SELECT id,meta," + instructionReadProjection + " FROM session_records"
	if open {
		query += " WHERE closed = 0"
	}
	res, err := db.Query(query + " ORDER BY id")
	if err != nil {
		return err
	}
	type record struct {
		id  string
		raw []byte
		err error
	}
	var records []record
	for res.Next() {
		var r record
		instructions, err := scanInstructionRead(res, &r.id, &r.raw)
		if err != nil {
			res.Close()
			return err
		}
		if instructions.hashes != [2]string{} {
			r.raw, r.err = instructions.join(r.raw)
			if r.err != nil {
				r.err = fmt.Errorf("%w: session %s: %v", ErrRecord, r.id, r.err)
			}
		}
		records = append(records, r)
	}
	if err := errors.Join(res.Err(), res.Close()); err != nil {
		return err
	}
	for _, r := range records {
		err := r.err
		if err == nil && !isObject(r.raw) {
			err = fmt.Errorf("%w: session %s record is not a bounded JSON object", ErrRecord, r.id)
		}
		if err := fn(r.id, r.raw, err); err != nil {
			return err
		}
	}
	return nil
}

// SafeIDs lists canonical session records in name order, as IDs does.
func SafeIDs(sessions string) ([]string, error) {
	return IDs(sessions)
}

// Revision changes whenever any stored session record is written or removed:
// it is the newest row's ID and revision, and the row count. The ID matters
// because deleting the newest row lets the next write reuse its revision.
// Callers cache what they build from every record (closed sessions included)
// by it.
func Revision(sessions string) (string, error) {
	db, ok, err := readable(sessions)
	if err != nil || !ok {
		return "", err
	}
	return revision(db)
}

// CacheRevision returns the all-record revision and delete counter as one
// token. The counter keeps a cache invalid when deleting the newest record
// lets a later write reuse its row revision, even for the same session ID.
func CacheRevision(sessions string) (string, error) {
	db, ok, err := readable(sessions)
	if err != nil || !ok {
		return "", err
	}
	rev, err := revision(db)
	if err != nil {
		return "", err
	}
	var deletes string
	if err := db.QueryRow("SELECT COALESCE((SELECT value FROM meta WHERE key = ?), '0')", DeletesKey).Scan(&deletes); err != nil {
		return "", err
	}
	return rev + "|d" + deletes, nil
}

func revision(db *sql.DB) (string, error) {
	var id string
	var top, rows int64
	err := db.QueryRow(`SELECT COALESCE((SELECT id FROM session_records ORDER BY revision DESC LIMIT 1), ''),
  COALESCE(MAX(revision), 0), COUNT(*) FROM session_records`).Scan(&id, &top, &rows)
	if err != nil {
		return "", err
	}
	return id + "/" + strconv.FormatInt(top, 10) + "/" + strconv.FormatInt(rows, 10), nil
}

// OpenWithPhase returns the IDs of the open sessions whose record holds an
// object under key, such as "transfer", in one of phases. It is one query
// over the session rows and reads no session folder.
func OpenWithPhase(sessions, key string, phases ...string) ([]string, error) {
	db, ok, err := readable(sessions)
	if err != nil || !ok || len(phases) == 0 {
		return nil, err
	}
	args := []any{"$." + key + ".phase"}
	for _, phase := range phases {
		args = append(args, phase)
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(phases)), ", ")
	var ids []string
	err = state.For(db).Read(context.Background(), func(r state.Reader) error {
		rows, err := r.QueryContext(context.Background(), "SELECT id FROM session_records WHERE closed = 0 AND json_extract(meta, ?) IN ("+marks+") ORDER BY id", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

// MoveSpawnItemTx updates only records that name the exact source item in
// this room. locked collects session locks before a retry; the caller must
// roll back when it returns false. SaveTx advances revisions for cached reads.
func MoveSpawnItemTx(conn *sql.Conn, room, goal, item, toGoal, toItem string, locked func(string) bool) error {
	ctx := context.Background()
	rows, err := conn.QueryContext(ctx, "SELECT id,meta,"+instructionReadProjection+" FROM session_records WHERE room_id=? AND json_extract(meta,'$.goal')=? AND json_extract(meta,'$.item')=?", room, goal, item)
	if err != nil {
		return err
	}
	type record struct{ id, meta string }
	var records []record
	for rows.Next() {
		var r record
		instructions, err := scanInstructionRead(rows, &r.id, &r.meta)
		if err != nil {
			rows.Close()
			return err
		}
		raw, err := instructions.join([]byte(r.meta))
		if err != nil {
			rows.Close()
			return err
		}
		r.meta = string(raw)
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, r := range records {
		if !locked(r.id) {
			continue
		}
		var fields map[string]json.RawMessage
		if err := decode(r.id, []byte(r.meta), &fields); err != nil {
			return err
		}
		fields["goal"], _ = json.Marshal(toGoal)
		if toItem == "" {
			delete(fields, "item")
		} else {
			fields["item"], _ = json.Marshal(toItem)
		}
		if err := SaveTx(conn, r.id, fields); err != nil {
			return err
		}
	}
	return nil
}

// RemapSpawnGoalsTx rewrites session records whose meta names a goal that
// folded into a parent goal, pointing them at the parent goal and item. It
// runs inside the caller's transaction and tolerates a missing table.
func RemapSpawnGoalsTx(conn *sql.Conn, remap func(room, goal string) (parent, item string, ok bool)) error {
	ctx := context.Background()
	var exists int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='session_records'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	rows, err := conn.QueryContext(ctx, "SELECT id,room_id,meta,"+instructionReadProjection+" FROM session_records")
	if err != nil {
		return err
	}
	type record struct {
		id, room, meta string
		instructions   instructionRead
	}
	var records []record
	for rows.Next() {
		var r record
		if r.instructions, err = scanInstructionRead(rows, &r.id, &r.room, &r.meta); err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range records {
		var meta map[string]any
		if err := json.Unmarshal([]byte(r.meta), &meta); err != nil || meta == nil {
			continue
		}
		goal, _ := meta["goal"].(string)
		if goal == "" {
			continue
		}
		parent, item, ok := remap(r.room, goal)
		if !ok {
			continue
		}
		raw, err := r.instructions.join([]byte(r.meta))
		if err != nil {
			return fmt.Errorf("%w: session %s: %v", ErrRecord, r.id, err)
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return fmt.Errorf("%w: session %s: %v", ErrRecord, r.id, err)
		}
		meta["goal"], meta["item"] = parent, item
		// SaveTx rewrites the meta with its typed columns and digest in one
		// statement; a direct meta update would drift the digest and make
		// every later ownership scan reject the record.
		if err := SaveTx(conn, r.id, meta); err != nil {
			return err
		}
	}
	return nil
}
