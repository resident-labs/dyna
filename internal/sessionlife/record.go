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
// The columns are what listings filter and sort on; meta is the whole record.
// revision grows with every write in the table, so a reader can tell whether
// its cached copy is current. session_records_closed covers the listing
// queries (every version, or open ones only), so they never read the wide
// meta rows. session_records_effective_name covers a lookup by address or
// name, which must not read every record either.
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
  meta TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS session_records_room ON session_records(room_id, closed);
CREATE UNIQUE INDEX IF NOT EXISTS session_records_revision ON session_records(revision);
CREATE INDEX IF NOT EXISTS session_records_closed ON session_records(closed, id, revision);
CREATE INDEX IF NOT EXISTS session_records_effective_name ON session_records(` + effectiveName + `, room);`

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
	if _, err := records(sessions); err != nil {
		return err
	}
	_, err := steeringDB(sessions)
	return err
}

// records opens dyna.db for the sessions folder and prepares the table, for
// writes. It takes the database writer when the table is new.
func records(sessions string) (*sql.DB, error) {
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		return nil, err
	}
	// Version 2 adds session_records_closed, version 3
	// session_records_effective_name; the schema is idempotent.
	err = database.Migrate(db, "sessions.records", 3, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), recordSchema)
		return err
	})
	if err != nil {
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

// readable opens dyna.db for reads. It never creates the table, so a read
// inside another store's transaction never waits for the writer; until the
// first write there are no rows, and readers use the legacy files.
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
	tables.Store(db, true)
	return db, true, nil
}

// querier is a database handle or a connection inside a transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// legacyOnly is a querier for a database without the record table: every
// record comes from its legacy file.
type legacyOnly struct{}

func (legacyOnly) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }

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
		return legacyLoadRaw(sessions, id)
	}
	return loadRaw(db, sessions, id)
}

func loadRaw(q querier, sessions, id string) ([]byte, Version, error) {
	if err := checkID(id); err != nil {
		return nil, "", err
	}
	if _, ok := q.(legacyOnly); ok {
		return legacyLoadRaw(sessions, id)
	}
	raw, version, err := loadStored(context.Background(), q, id)
	if errors.Is(err, sql.ErrNoRows) {
		return legacyLoadRaw(sessions, id)
	}
	return raw, version, err
}

// LoadStored decodes a canonical record from the caller's database snapshot.
// It never imports or reads a legacy file; a missing row returns sql.ErrNoRows.
func LoadStored(ctx context.Context, q state.Reader, id string, v any) error {
	if err := checkID(id); err != nil {
		return err
	}
	raw, _, err := loadStored(ctx, q, id)
	if err != nil {
		return err
	}
	return decode(id, raw, v)
}

func loadStored(ctx context.Context, q querier, id string) ([]byte, Version, error) {
	var meta string
	var revision int64
	err := q.QueryRowContext(ctx, "SELECT meta, revision FROM session_records WHERE id = ?", id).Scan(&meta, &revision)
	if err != nil {
		return nil, "", err
	}
	return []byte(meta), rowVersion(revision), nil
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
		return legacyVersion(sessions, id)
	}
	var revision int64
	err = db.QueryRow("SELECT revision FROM session_records WHERE id = ?", id).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return legacyVersion(sessions, id)
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
	return writeRow(conn, id, raw, "ON CONFLICT(id) DO UPDATE SET revision=excluded.revision, room_id=excluded.room_id, room=excluded.room, name=excluded.name, status=excluded.status, closed=excluded.closed, parent=excluded.parent, created_ns=excluded.created_ns, activity_ms=excluded.activity_ms, meta=excluded.meta")
}

// recordColumns are the record fields copied into columns.
type recordColumns struct {
	Snapshot
	RoomID        string    `json:"roomId"`
	Room          string    `json:"room"`
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	ParentSession string    `json:"parentSession"`
	Created       time.Time `json:"created"`
	LastActivity  time.Time `json:"lastActivity"`
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
	_, err := conn.ExecContext(context.Background(), `INSERT INTO session_records(id, revision, room_id, room, name, status, closed, parent, created_ns, activity_ms, meta)
VALUES (?, (SELECT COALESCE(MAX(revision), 0) + 1 FROM session_records), ?, ?, ?, ?, ?, ?, ?, ?, ?) `+conflict,
		id, c.RoomID, c.Room, c.Name, c.Status, closed, c.ParentSession, created, c.LastActivity.UnixMilli(), string(raw))
	return err
}

func isObject(raw []byte) bool {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return c == '{'
	}
	return false
}

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
	return state.For(db).Write(context.Background(), Changed(id), func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(context.Background(), "DELETE FROM session_records WHERE id = ?", id); err != nil {
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
	})
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
	for _, table := range []string{"session_turns", "session_queues"} {
		ok, err := tableExists(conn, table)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE session = ?", id); err != nil {
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

// IDs lists the sessions in name order: every record, and every session
// folder under sessions, whatever its name, so careful readers see damaged
// or stray folders too. A folder entry that is a symlink could lead out of
// sessions: it is left out, and IDs still returns the others with an
// ErrRecord error. Careful readers stop on that error; others may use the
// safe IDs (see SafeIDs).
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
// listing can check its cached copies without a query per session. A folder
// without a record or legacy file has an empty version.
func Versions(sessions string) (map[string]Version, error) {
	return versions(sessions, versionsQuery)
}

// OpenVersions is Versions for open sessions only: the rows whose record is
// not closed, read through session_records_closed without touching closed
// rows, and the session folders that have no row yet.
func OpenVersions(sessions string) (map[string]Version, error) {
	return versions(sessions, openQuery)
}

func versions(sessions, query string) (map[string]Version, error) {
	db, ok, err := readable(sessions)
	if err != nil {
		return nil, err
	}
	// Folders first: a session started meanwhile has its folder before its
	// row, so one of the two reads finds it.
	legacy, err := legacyFolders(db, ok, sessions)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Version, len(legacy.ids))
	for _, id := range legacy.ids {
		out[id], _ = legacyVersion(sessions, id)
	}
	if ok {
		if err := scanVersions(db, query, out); err != nil {
			return nil, err
		}
	}
	return out, legacy.unsafe
}

// The listing queries read only session_records_closed (see recordSchema).
const (
	versionsQuery = "SELECT id, revision FROM session_records"
	openQuery     = "SELECT id, revision FROM session_records WHERE closed = 0"
)

func readVersions(db *sql.DB, out map[string]Version) error {
	return scanVersions(db, versionsQuery, out)
}

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

// Each calls fn with every session record in name order: one query for the
// rows, then the legacy files of session folders that have no row yet. With
// open, closed records are left out by the query. A record that cannot be
// read reaches fn with its error; fn decides whether that stops the scan.
// Folder entries that are symlinks are left out and returned as an ErrRecord
// error at the end, as IDs does.
func Each(sessions string, open bool, fn func(id string, raw []byte, err error) error) error {
	db, ok, err := readable(sessions)
	if err != nil {
		return err
	}
	// One query: a session that reopens between two queries would be in
	// neither, and fall back to an old file.
	rows := map[string][]byte{}
	listed := map[string]bool{} // closed rows left out by open
	if ok {
		res, err := db.Query("SELECT id, closed != 0, CASE WHEN closed != 0 AND ? THEN '' ELSE meta END FROM session_records", open)
		if err != nil {
			return err
		}
		for res.Next() {
			var id, meta string
			var closed bool
			if err := res.Scan(&id, &closed, &meta); err != nil {
				res.Close()
				return err
			}
			if closed && open {
				listed[id] = true
			} else {
				rows[id] = []byte(meta)
			}
		}
		if err := errors.Join(res.Err(), res.Close()); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(sessions)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var unsafe error
	legacy := map[string]bool{}
	for _, e := range entries {
		id := e.Name()
		switch {
		case e.Type()&os.ModeSymlink != 0:
			unsafe = errors.Join(unsafe, fmt.Errorf("%w: session entry %q is a symlink", ErrRecord, id))
		case e.IsDir() && rows[id] == nil && !listed[id]:
			legacy[id] = true
		}
	}
	ids := make([]string, 0, len(rows)+len(legacy))
	for id := range rows {
		ids = append(ids, id)
	}
	for id := range legacy {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw, err := rows[id], error(nil)
		if raw == nil {
			if raw, _, err = legacyLoadRaw(sessions, id); errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		if err := fn(id, raw, err); err != nil {
			return err
		}
	}
	return unsafe
}

// SafeIDs is IDs for readers that skip unsafe entries rather than stop: only
// a failure to list the sessions is an error.
func SafeIDs(sessions string) ([]string, error) {
	ids, err := IDs(sessions)
	if errors.Is(err, ErrRecord) {
		err = nil
	}
	return ids, err
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
	rows, err := conn.QueryContext(ctx, "SELECT id, meta FROM session_records WHERE room_id=? AND json_extract(meta,'$.goal')=? AND json_extract(meta,'$.item')=?", room, goal, item)
	if err != nil {
		return err
	}
	type record struct{ id, meta string }
	var records []record
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.id, &r.meta); err != nil {
			rows.Close()
			return err
		}
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
	rows, err := conn.QueryContext(ctx, "SELECT id, room_id, meta FROM session_records")
	if err != nil {
		return err
	}
	type record struct{ id, room, meta string }
	var records []record
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.id, &r.room, &r.meta); err != nil {
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
		meta["goal"], meta["item"] = parent, item
		body, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "UPDATE session_records SET meta=? WHERE id=?", string(body), r.id); err != nil {
			return err
		}
	}
	return nil
}
