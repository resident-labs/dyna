package goals

import (
	"bytes"
	"context"
	"database/sql"
	"dyna-agent/internal/sessionlife"
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
	"dyna-agent/internal/store"
)

// Each goal is one row; open goals read through goals_state.
// goal_rooms holds the rest of a room's state, and the Global rule set
// under room "*".
const goalsSchema = `CREATE TABLE IF NOT EXISTS goals (
 room TEXT NOT NULL, id TEXT NOT NULL, state TEXT NOT NULL,
 body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(room,id)
);
CREATE INDEX IF NOT EXISTS goals_state ON goals(state,room);
CREATE TABLE IF NOT EXISTS goal_rooms (room TEXT PRIMARY KEY, body TEXT NOT NULL CHECK(json_valid(body)));`

// goal_items holds one row per item, under its own schema key so the table
// can grow apart from the goal body schema.
const itemsSchema = `CREATE TABLE IF NOT EXISTS goal_items (
 room TEXT NOT NULL, goal TEXT NOT NULL, id TEXT NOT NULL, text TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('open','done','dropped')),
 assignee TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL,
 PRIMARY KEY(room,goal,id)
)`

// ready records handles whose goal tables and import are in place.
var ready sync.Map // *sql.DB -> bool

// Migrate brings the goal tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := (&Store{Root: filepath.Join(dataDir, "goals")}).open()
	return err
}

func (s *Store) open() (*sql.DB, error) {
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		return nil, err
	}
	if _, ok := ready.Load(db); ok {
		return db, nil
	}
	if err := s.migrate(db); err != nil {
		return nil, err
	}
	ready.Store(db, true)
	return db, nil
}

// migrate creates the goal tables, imports state.json once and folds legacy
// child goals into items, and adds room revisions (goals.schema=3).
func (s *Store) migrate(db *sql.DB) error {
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	// Old writers hold state.lock while they replace state.json. Take it
	// before the database transaction: no file lock is taken inside one.
	unlock, err := store.Lock(filepath.Join(s.Root, "state.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	// goal_items migrates first: the schema-2 fold inserts item rows.
	if err := database.Migrate(db, "goal_items.schema", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), itemsSchema)
		return err
	}); err != nil {
		return err
	}
	if err := database.Migrate(db, "goals.schema", 3, func(conn *sql.Conn, from int) error {
		ctx := context.Background()
		if from < 1 {
			if _, err := conn.ExecContext(ctx, goalsSchema); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO meta(key,value) VALUES ('goals.revision','0')"); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "ALTER TABLE goal_rooms ADD COLUMN revision INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "UPDATE goal_rooms SET revision=(SELECT CAST(value AS INTEGER) FROM meta WHERE key='goals.revision')"); err != nil {
			return err
		}
		if from < 1 {
			if err := s.importLegacy(conn); err != nil {
				return err
			}
		}
		if from < 2 {
			return s.foldLegacyChildren(conn)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// TODO: legacy/shim remove later — copies goals/state.json into dyna.db once and leaves the file in place so an older binary can roll back; remove after every host has run a SQLite build for a while.
func (s *Store) importLegacy(conn *sql.Conn) error {
	st, err := readLegacy(filepath.Join(s.Root, "state.json"))
	if err != nil {
		return err
	}
	_, err = saveState(conn, rows{}, st)
	return err
}

// rows holds the stored JSON body of each row, keyed by room and goal ID.
// Update compares with it to write only changed rows.
type rows struct {
	goals map[[2]string][]byte
	rooms map[string][]byte
}

type reader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// allGoals, openGoals and noGoals select goal rows for readState and the
// scoped writers. Scoped writers add the closed goals they name.
const (
	allGoals  = "1"
	openGoals = "state='active'"
	noGoals   = "0"
)

// readState reads every room row and the goals matching where. A state read
// with fewer goals saves only the goals it holds.
func readState(query reader, where string, args ...any) (State, rows, error) {
	return readStateRooms(query, where, nil, args...)
}

// A nil room list reads all rooms. A scoped read also includes Global rules.
func readStateRooms(query reader, where string, rooms []string, args ...any) (State, rows, error) {
	roomQuery := "SELECT room,body FROM goal_rooms"
	var roomArgs []any
	if rooms != nil {
		for _, id := range append(append([]string{}, rooms...), Global) {
			roomArgs = append(roomArgs, id)
		}
		filter := "room IN (" + strings.TrimSuffix(strings.Repeat("?,", len(roomArgs)), ",") + ")"
		roomQuery += " WHERE " + filter
		where = "(" + where + ") AND " + filter
		args = append(append([]any{}, args...), roomArgs...)
	}
	st := State{Version: 1, Rooms: map[string]*RoomState{}}
	stored := rows{goals: map[[2]string][]byte{}, rooms: map[string][]byte{}}
	err := scan(query, roomQuery, func(room string, body []byte) error {
		stored.rooms[room] = body
		if room == Global {
			st.Global = &RuleSet{}
			return json.Unmarshal(body, st.Global)
		}
		r := &RoomState{}
		if err := json.Unmarshal(body, r); err != nil {
			return err
		}
		r.Goals = map[string]*Goal{}
		st.Rooms[room] = r
		return nil
	}, roomArgs...)
	if err != nil {
		return st, stored, err
	}
	err = scan(query, "SELECT room,body FROM goals WHERE "+where, func(room string, body []byte) error {
		g := &Goal{}
		if err := json.Unmarshal(body, g); err != nil {
			return err
		}
		stored.goals[[2]string{room, g.ID}] = body
		st.Room(room).Goals[g.ID] = g
		return nil
	}, args...)
	return st, stored, err
}

func scan(query reader, sqlText string, each func(room string, body []byte) error, args ...any) error {
	result, err := query.QueryContext(context.Background(), sqlText, args...)
	if err != nil {
		return err
	}
	defer result.Close()
	for result.Next() {
		var room string
		var body []byte
		if err := result.Scan(&room, &body); err != nil {
			return err
		}
		if err := each(room, body); err != nil {
			return err
		}
	}
	return result.Err()
}

// saveState writes the rows whose JSON differs from stored and deletes the
// rows st no longer has. Callers hold BEGIN IMMEDIATE.
func saveState(conn *sql.Conn, stored rows, st State) (bool, error) {
	ctx := context.Background()
	var revision int64
	if err := conn.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER)+1 FROM meta WHERE key='goals.revision'").Scan(&revision); err != nil {
		return false, err
	}
	changed := false
	changedRooms := map[string]bool{}
	exec := func(query string, args ...any) error {
		changed = true
		_, err := conn.ExecContext(ctx, query, args...)
		return err
	}
	roomBodies := map[string]any{}
	for id, r := range st.Rooms {
		roomBodies[id] = roomRow{RuleSet: r.RuleSet, Keeper: r.Keeper, NextID: r.NextID, Notices: r.Notices, ReleasedAt: r.ReleasedAt}
	}
	if st.Global != nil {
		roomBodies[Global] = st.Global
	}
	for id, v := range roomBodies {
		body, err := json.Marshal(v)
		if err != nil {
			return false, err
		}
		if old, ok := stored.rooms[id]; ok && bytes.Equal(old, body) {
			continue
		}
		if err := exec("INSERT INTO goal_rooms(room,body,revision) VALUES (?,?,?) ON CONFLICT(room) DO UPDATE SET body=excluded.body,revision=excluded.revision", id, string(body), revision); err != nil {
			return false, err
		}
	}
	for id := range stored.rooms {
		if _, ok := roomBodies[id]; !ok {
			if err := exec("DELETE FROM goal_rooms WHERE room=?", id); err != nil {
				return false, err
			}
		}
	}
	for room, r := range st.Rooms {
		for id, g := range r.Goals {
			body, err := json.Marshal(g)
			if err != nil {
				return false, err
			}
			if old, ok := stored.goals[[2]string{room, id}]; ok && bytes.Equal(old, body) {
				continue
			}
			changedRooms[room] = true
			if err := exec("INSERT INTO goals(room,id,state,body) VALUES (?,?,?,?) ON CONFLICT(room,id) DO UPDATE SET state=excluded.state,body=excluded.body", room, id, g.State, string(body)); err != nil {
				return false, err
			}
		}
	}
	for key := range stored.goals {
		if r := st.Rooms[key[0]]; r == nil || r.Goals[key[1]] == nil {
			changedRooms[key[0]] = true
			if err := exec("DELETE FROM goals WHERE room=? AND id=?", key[0], key[1]); err != nil {
				return false, err
			}
		}
	}
	for room := range changedRooms {
		if _, err := conn.ExecContext(ctx, "UPDATE goal_rooms SET revision=? WHERE room=?", revision, room); err != nil {
			return false, err
		}
	}
	if changed {
		if _, err := conn.ExecContext(ctx, "UPDATE meta SET value=? WHERE key='goals.revision'", revision); err != nil {
			return false, err
		}
	}
	return changed, nil
}

// roomRow is a room's stored state without its goals.
type roomRow struct {
	RuleSet
	ReleasedAt map[string]time.Time `json:"releasedAt,omitempty"`
	Keeper     KeeperRoomState      `json:"keeper,omitempty"`
	NextID     int                  `json:"nextId"`
	Notices    []Notice             `json:"notices"`
}

// Read returns a WAL snapshot of the whole store without waiting for writers.
func (s *Store) Read() (State, error) { return s.read(false, allGoals) }

// ReadOpen is Read with only open goals.
func (s *Store) ReadOpen() (State, error) { return s.read(false, openGoals) }

// ReadLocked waits for any writer transaction before reading the state.
func (s *Store) ReadLocked() (State, error) { return s.read(true, allGoals) }

func (s *Store) read(locked bool, where string, args ...any) (State, error) {
	db, err := s.open()
	if err != nil {
		return State{}, err
	}
	var st State
	if locked {
		err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error { var err error; st, _, err = readState(conn, where, args...); return err })
		s.dropExpired(&st)
		return st, err
	}
	err = state.For(db).Read(context.Background(), func(query state.Reader) error {
		var err error
		st, _, err = readState(query, where, args...)
		return err
	})
	s.dropExpired(&st)
	return st, err
}

// Update runs fn on the whole state in one writer transaction and writes the
// rows it changed. A change also moves Revision.
func (s *Store) Update(fn func(*State) error) error { return s.update(fn, allGoals) }

// UpdateOpen is Update on a state with only open goals. Use it for room
// rows and open work; it cannot see closed goals.
func (s *Store) UpdateOpen(fn func(*State) error) error { return s.update(fn, openGoals) }

func (s *Store) update(fn func(*State) error, where string, args ...any) error {
	return s.updateTx(func(_ *wake, st *State) error { return fn(st) }, where, args...)
}

// errNeedLock rolls back a goal transaction that wants to wake a session
// whose lock it does not hold yet.
var errNeedLock = errors.New("goal change needs more session locks")

// wake wakes and checks goal holders inside a goal transaction. A session is
// woken only while its session lock is held: its own metadata updates read,
// then write under that lock, and a wake between them would be lost. File
// locks cannot be taken inside the transaction, so a wake of an unlocked
// session rolls the transaction back; updateTx takes the locks and runs it
// again.
type wake struct {
	conn     *sql.Conn
	sessions string
	locked   map[string]bool
	need     []string
}

// activate wakes a holder. A session whose lock is not held yet is only
// noted: fn runs on as if it woke, so one pass collects every lock it needs,
// and updateTx rolls the pass back. Other IDs ("owner") need no lock.
func (w *wake) activate(id string) error {
	if !w.lock(id) {
		return nil
	}
	return sessionlife.ActivateTx(w.conn, w.sessions, id)
}

// lock collects the locks needed for a metadata change before the next pass.
func (w *wake) lock(id string) bool {
	if sessionlife.IsSessionID(id) && !w.locked[id] {
		w.need = append(w.need, id)
		return false
	}
	return true
}

// check reads a holder's lifecycle; it needs no lock.
func (w *wake) check(id string) error { return sessionlife.CheckTx(w.conn, w.sessions, id) }

// updateTx is update for changes that wake goal holders.
func (s *Store) updateTx(fn func(*wake, *State) error, where string, args ...any) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	locked := map[string]bool{}
	for {
		ids := make([]string, 0, len(locked))
		for id := range locked {
			ids = append(ids, id)
		}
		var need []string
		err := sessionlife.WithSessionLocks(s.sessions(), ids, func() error {
			// TODO: legacy/shim remove later — roomsvc.Watch publishes goals changes; publish here after item 16 removes that scan.
			return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
				st, stored, err := readState(conn, where, args...)
				if err != nil {
					return err
				}
				w := &wake{conn: conn, sessions: s.sessions(), locked: locked}
				err = fn(w, &st)
				// A missed wake rolls back even when fn treated it as "not live".
				if len(w.need) > 0 {
					need = w.need
					return errNeedLock
				}
				if err != nil {
					return err
				}
				if err := recordReleases(&st, stored, s.now()); err != nil {
					return err
				}
				_, err = saveState(conn, stored, st)
				return err
			})
		})
		if !errors.Is(err, errNeedLock) {
			return err
		}
		// Retry while the lock set grows; it is bounded by the IDs in the
		// state, so a pass that asks for nothing new is a bug, not a wait.
		grew := false
		for _, id := range need {
			grew = grew || !locked[id]
			locked[id] = true
		}
		if !grew {
			return fmt.Errorf("goal change kept asking for the same session locks: %v", need)
		}
	}
}

// list reads the goals matching where, oldest first.
func (s *Store) list(locked bool, where string, args ...any) ([]Goal, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	out := []Goal{}
	each := func(_ string, body []byte) error {
		var g Goal
		if err := json.Unmarshal(body, &g); err != nil {
			return err
		}
		out = append(out, g)
		return nil
	}
	query := "SELECT room,body FROM goals WHERE " + where
	if locked {
		err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error { return scan(conn, query, each, args...) })
	} else {
		err = scan(db, query, each, args...)
	}
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].dropExpired(s.now())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt) || out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].ID < out[j].ID
	})
	return out, nil
}

// Revision changes only when an Update writes a row.
func (s *Store) Revision() (string, error) {
	db, err := s.open()
	if err != nil {
		return "", err
	}
	var revision string
	err = db.QueryRow("SELECT value FROM meta WHERE key='goals.revision'").Scan(&revision)
	return revision, err
}

// RoomRevisions identifies rooms changed by committed writes without decoding goals.
func (s *Store) RoomRevisions() (map[string]string, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT room,revision FROM goal_rooms")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	revisions := map[string]string{}
	for rows.Next() {
		var room, revision string
		if err := rows.Scan(&room, &revision); err != nil {
			return nil, err
		}
		revisions[room] = revision
	}
	return revisions, rows.Err()
}

// ReadRooms reads the named rooms and Global rules from one database snapshot.
func (s *Store) ReadRooms(rooms []string) (State, error) {
	db, err := s.open()
	if err != nil {
		return State{}, err
	}
	var st State
	err = state.For(db).Read(context.Background(), func(query state.Reader) error {
		var err error
		st, _, err = readStateRooms(query, allGoals, append([]string{}, rooms...))
		return err
	})
	s.dropExpired(&st)
	return st, err
}

// recordReleases runs in the shared writer so closing, reassigning and removing
// holders record the same boundary, including scoped writes and handovers.
func recordReleases(st *State, stored rows, now time.Time) error {
	for key, body := range stored.goals {
		r := st.Rooms[key[0]]
		if r == nil {
			continue
		}
		var old Goal
		if err := json.Unmarshal(body, &old); err != nil {
			return err
		}
		if !old.Open() {
			continue
		}
		fresh := r.Goals[key[1]]
		for _, holder := range old.Holders {
			if fresh != nil && fresh.Open() && fresh.HeldBy(holder) {
				continue
			}
			if r.ReleasedAt == nil {
				r.ReleasedAt = map[string]time.Time{}
			}
			r.ReleasedAt[holder] = now
		}
	}
	return nil
}

// legacyBody reads a stored goal body, including the parent field version 2
// removes.
type legacyBody struct {
	Goal
	Parent string `json:"parent"`
}

// foldLegacyChildren folds every open child goal into an item on its parent,
// deepest first in numeric goal order, and rewrites session records and
// keeper journals in the same transaction. A child whose parent is closed or
// missing becomes a standalone goal. Rerunning over a folded store changes
// nothing: no parent links remain.
func (s *Store) foldLegacyChildren(conn *sql.Conn) error {
	ctx := context.Background()
	now := s.now()
	byKey := map[[2]string]*legacyBody{}
	var order [][2]string
	if err := scan(conn, "SELECT room,body FROM goals", func(room string, body []byte) error {
		var g legacyBody
		if err := json.Unmarshal(body, &g); err != nil {
			return err
		}
		g.Room = room
		byKey[[2]string{room, g.ID}] = &g
		order = append(order, [2]string{room, g.ID})
		return nil
	}); err != nil {
		return err
	}
	depth := func(room, id string) int {
		n := 0
		for n <= len(byKey) {
			g := byKey[[2]string{room, id}]
			if g == nil || g.Parent == "" {
				return n
			}
			id = g.Parent
			n++
		}
		return n
	}
	// remap tracks, per room, which child goal became which parent item.
	remap := map[string]map[string][2]string{}
	remember := func(room, child, parent, item string) {
		if remap[room] == nil {
			remap[room] = map[string][2]string{}
		}
		remap[room][child] = [2]string{parent, item}
	}
	liveHolder := func(g *legacyBody) string {
		holders := append([]string{}, g.Holders...)
		sort.Strings(holders)
		for _, id := range holders {
			var status string
			err := conn.QueryRowContext(ctx, "SELECT status FROM session_records WHERE id=?", id).Scan(&status)
			if err == nil && status != "closed" {
				return id
			}
		}
		return ""
	}
	writeGoal := func(g *legacyBody) error {
		body, err := json.Marshal(g.Goal)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, "UPDATE goals SET state=?, body=? WHERE room=? AND id=?", g.State, string(body), g.Room, g.ID)
		return err
	}
	// Children fold deepest first, numeric goal order within a level.
	var children []*legacyBody
	for _, key := range order {
		if g := byKey[key]; g.Parent != "" {
			children = append(children, g)
		}
	}
	num := func(id string) int {
		n, _ := strconv.Atoi(strings.TrimPrefix(id, "g"))
		return n
	}
	sort.Slice(children, func(i, j int) bool {
		gi, gj := children[i], children[j]
		di, dj := depth(gi.Room, gi.ID), depth(gj.Room, gj.ID)
		if di != dj {
			return di > dj
		}
		if gi.Room != gj.Room {
			return gi.Room < gj.Room
		}
		return num(gi.ID) < num(gj.ID)
	})
	dirty := map[*legacyBody]bool{}
	for _, child := range children {
		if !child.Open() {
			continue
		}
		parent := byKey[[2]string{child.Room, child.Parent}]
		if parent == nil || !parent.Open() {
			child.Parent = ""
			if err := writeGoal(child); err != nil {
				return err
			}
			continue
		}
		assignee := liveHolder(child)
		parent.NextItem++
		dirty[parent] = true
		itemID := fmt.Sprintf("i%d", parent.NextItem)
		text := child.Text
		if child.Detail != "" {
			text += "\n\nBrief:\n" + child.Detail
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO goal_items(room,goal,id,text,state,assignee,updated_at) VALUES (?,?,?,?,?,?,?)",
			child.Room, parent.ID, itemID, text, ItemOpen, assignee, now.UnixMilli()); err != nil {
			return err
		}
		// Items already on the child, from deeper folds, move with it.
		moved, err := conn.QueryContext(ctx, "SELECT id FROM goal_items WHERE room=? AND goal=?"+itemOrder, child.Room, child.ID)
		if err != nil {
			return err
		}
		var itemIDs []string
		for moved.Next() {
			var id string
			if err := moved.Scan(&id); err != nil {
				moved.Close()
				return err
			}
			itemIDs = append(itemIDs, id)
		}
		if err := moved.Err(); err != nil {
			return err
		}
		movedMap := map[string]string{}
		for _, id := range itemIDs {
			parent.NextItem++
			next := fmt.Sprintf("i%d", parent.NextItem)
			if _, err := conn.ExecContext(ctx, "UPDATE goal_items SET goal=?, id=?, updated_at=? WHERE room=? AND goal=? AND id=?",
				parent.ID, next, now.UnixMilli(), child.Room, child.ID, id); err != nil {
				return err
			}
			movedMap[id] = next
		}
		remember(child.Room, child.ID, parent.ID, itemID)
		// Deeper children that folded into this one now point at the parent.
		for old, next := range movedMap {
			for c, pair := range remap[child.Room] {
				if pair[0] == child.ID && pair[1] == old {
					remember(child.Room, c, parent.ID, next)
				}
			}
		}
		child.Parent = ""
		child.State = "dropped"
		child.Done = &Outcome{At: now, By: "migration", Summary: "migrated into parent " + parent.ID + " as item " + itemID}
		if err := writeGoal(child); err != nil {
			return err
		}
	}
	// Every remaining parent link strips as the body rewrites.
	for _, key := range order {
		if g := byKey[key]; g.Parent != "" {
			g.Parent = ""
			dirty[g] = true
		}
	}
	// A parent that only gained items also persists its new counter, so a
	// later AddItem cannot reuse a migrated item ID.
	for g := range dirty {
		if err := writeGoal(g); err != nil {
			return err
		}
	}
	if err := sessionlife.RemapSpawnGoalsTx(conn, func(room, goal string) (string, string, bool) {
		if pair, ok := remap[room][goal]; ok {
			return pair[0], pair[1], true
		}
		return "", "", false
	}); err != nil {
		return err
	}
	return s.convertJournals()
}

// convertJournals rewrites each room's journal so every keeper entry becomes
// one keeper-status event; event lines stay byte-for-byte. A journal without
// keeper entries is not rewritten.
func (s *Store) convertJournals() error {
	files, err := filepath.Glob(filepath.Join(s.Root, "journal", "*.jsonl"))
	if err != nil || len(files) == 0 {
		return err
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var out []byte
		changed := false
		for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var kind struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(line, &kind) == nil && kind.Kind == "event" {
				out = append(out, line...)
				out = append(out, '\n')
				continue
			}
			var e JournalEntry
			if err := json.Unmarshal(line, &e); err != nil {
				out = append(out, line...)
				out = append(out, '\n')
				continue
			}
			goal := ""
			if len(e.Entries) > 0 && len(e.Entries[0].Goals) > 0 {
				goal = e.Entries[0].Goals[0]
			}
			texts := make([]string, len(e.Entries))
			for i, l := range e.Entries {
				texts[i] = l.Agent + " " + l.Status + ": " + l.Summary
			}
			ev := Event{Goal: goal, By: "keeper", Kind: "event", Event: "keeper-status", Text: strings.Join(texts, "; "), At: e.At}
			body, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			out = append(out, body...)
			out = append(out, '\n')
			changed = true
		}
		if !changed {
			continue
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, out, 0600); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return nil
}
