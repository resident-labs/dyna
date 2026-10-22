package goals

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife"
)

var seedRevision int

// seedSession inserts one session_records row the fold can read.
func seedSession(t *testing.T, db *sql.DB, id, room, status, goal string) {
	t.Helper()
	seedRevision++
	meta := map[string]any{"id": id, "roomId": room, "status": status}
	if goal != "" {
		meta["goal"] = goal
	}
	body, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO session_records(id,revision,room_id,room,name,status,closed,parent,created_ns,activity_ms,meta) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
		id, seedRevision, room, room, strings.TrimPrefix(id, "ses_20261021-120000-"), status, 0, "", 1, 1, string(body))
	if err != nil {
		t.Fatal(err)
	}
}

func sessionGoal(t *testing.T, db *sql.DB, id string) (goal, item string) {
	t.Helper()
	var body string
	if err := db.QueryRow("SELECT meta FROM session_records WHERE id=?", id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Goal string `json:"goal"`
		Item string `json:"item"`
	}
	if err := json.Unmarshal([]byte(body), &meta); err != nil {
		t.Fatal(err)
	}
	return meta.Goal, meta.Item
}

// Schema 2 folds open child goals into parent items, deepest first, and
// remaps session records and the journal in the same pass. A rerun over a
// converted store changes nothing.
func TestSchema2FoldsChildGoalsIntoItems(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: filepath.Join(root, "goals"), Now: func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }}
	if err := sessionlife.Prepare(filepath.Join(root, "sessions")); err != nil {
		t.Fatal(err)
	}
	db, err := openGoalDB(s)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A goals.schema=1 database with parent links, built with raw SQL so no
	// store method runs the version-2 migration first.
	if _, err := db.Exec("CREATE TABLE goals (\n room TEXT NOT NULL, id TEXT NOT NULL, state TEXT NOT NULL,\n body TEXT NOT NULL, PRIMARY KEY(room,id)\n)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE goal_rooms (room TEXT PRIMARY KEY, body TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta(key,value) VALUES ('goals.schema','1')"); err != nil {
		t.Fatal(err)
	}
	seedSession(t, db, "ses_20261021-120000-00000000000a", "r", "idle", "")
	seedSession(t, db, "ses_20261021-120000-00000000000b", "r", "idle", "")
	seedSession(t, db, "ses_20261021-120000-00000000000c", "r", "idle", "")
	insert := func(id, text, state, parent string, holders ...string) {
		t.Helper()
		now := "2026-10-21T11:00:00Z"
		body := map[string]any{"id": id, "text": text, "setter": "owner", "holders": holders, "state": state, "createdAt": now, "updatedAt": now, "edits": []any{}}
		if parent != "" {
			body["parent"] = parent
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO goals(room,id,state,body) VALUES ('r',?,?,?)", id, state, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	// g1 parent, g2 child of g1, g3 grandchild of g2, g4 closed child of g1,
	// g5 child of the closed g4 (orphan: stays standalone). sesC closes after
	// the records exist, so g3's only holder is closed at fold time.
	insert("g1", "Parent", "active", "", "ses_20261021-120000-00000000000a")
	insert("g2", "Child", "active", "g1", "ses_20261021-120000-00000000000b")
	insert("g3", "Grandchild", "active", "g2", "ses_20261021-120000-00000000000c")
	insert("g4", "Closed child", "done", "g1", "ses_20261021-120000-00000000000b")
	insert("g5", "Orphan", "active", "g4", "ses_20261021-120000-00000000000b")
	if _, err := db.Exec("UPDATE session_records SET status='closed', closed=1 WHERE id=?", "ses_20261021-120000-00000000000c"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE session_records SET meta=json_set(meta,'$.goal','g2') WHERE id=?", "ses_20261021-120000-00000000000b"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE session_records SET meta=json_set(meta,'$.goal','g3') WHERE id=?", "ses_20261021-120000-00000000000c"); err != nil {
		t.Fatal(err)
	}
	g1, g2, g3, g4, g5 := Goal{ID: "g1"}, Goal{ID: "g2"}, Goal{ID: "g3"}, Goal{ID: "g4"}, Goal{ID: "g5"}

	journal := filepath.Join(s.Root, "journal", "r.jsonl")
	if err := os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
		t.Fatal(err)
	}
	entry, err := json.Marshal(JournalEntry{ID: "j0", At: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), Entries: []JournalLine{{Agent: "a", Status: "on_track", Summary: "Fine", Goals: []string{"g1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	converted := `{"id":"e0","kind":"event","goal":"g1","by":"keeper","event":"keeper-status","text":"already converted","at":"2026-10-20T13:00:00Z"}`
	if err := os.WriteFile(journal, append(append(entry, '\n'), append([]byte(converted), '\n')...), 0600); err != nil {
		t.Fatal(err)
	}

	if err := s.migrate(db); err != nil {
		t.Fatal(err)
	}

	// The deepest goal folds first, so g3 becomes an item of g2 before g2
	// folds into g1; both end up as items of g1, in numeric goal order.
	items, err := s.Items("r", g1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("g1 items: %+v", items)
	}
	if items[0].ID != "i1" || items[0].Text != "Child" || items[0].Assignee != "ses_20261021-120000-00000000000b" {
		t.Fatalf("i1 = %+v", items[0])
	}
	if items[1].ID != "i2" || items[1].Text != "Grandchild" || items[1].Assignee != "" {
		t.Fatalf("i2 = %+v", items[1])
	}
	for _, g := range []Goal{g1, g2, g3, g4, g5} {
		var body string
		if err := db.QueryRow("SELECT body FROM goals WHERE room='r' AND id=?", g.ID).Scan(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body, `"parent"`) {
			t.Fatalf("%s keeps a parent field: %s", g.ID, body)
		}
	}
	if fresh, _ := s.Get("r", g2.ID); fresh.State != "dropped" || fresh.Done == nil || !strings.Contains(fresh.Done.Summary, "migrated into parent "+g1.ID) {
		t.Fatalf("g2 = %+v", fresh)
	}
	if fresh, _ := s.Get("r", g5.ID); fresh.State != "active" {
		t.Fatalf("orphan g5 = %+v", fresh)
	}
	// Session records point at the parent goal and its item.
	if goal, item := sessionGoal(t, db, "ses_20261021-120000-00000000000b"); goal != g1.ID || item != "i1" {
		t.Fatalf("sesB -> %s %s", goal, item)
	}
	if goal, item := sessionGoal(t, db, "ses_20261021-120000-00000000000c"); goal != g1.ID || item != "i2" {
		t.Fatalf("sesC -> %s %s", goal, item)
	}
	// The journal holds only events: the old entry converted, the converted
	// line untouched.
	events, err := s.Events("r", g1.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var statuses, notes int
	for _, ev := range events {
		switch {
		case ev.Event == "keeper-status" && strings.Contains(ev.Text, "already converted"):
			notes++
		case ev.Event == "keeper-status":
			statuses++
		}
	}
	if statuses != 1 || notes != 1 {
		t.Fatalf("events: %+v", events)
	}
	if entries, _, err := s.Journal("r", "", 0); err != nil || len(entries) != 0 {
		t.Fatalf("entries after conversion: %v err=%v", entries, err)
	}

	// The fold is idempotent: a second pass changes nothing.
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(db); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("second migration rewrote the journal")
	}
	items2, err := s.Items("r", g1.ID)
	if err != nil || len(items2) != 2 {
		t.Fatalf("second migration moved items: %v err=%v", items2, err)
	}
	// The parent's item counter survived the fold, so a later add cannot
	// reuse a migrated item ID and overwrite it (review R3).
	late, err := s.AddItem("r", g1.ID, "owner", "late", "")
	if err != nil || late.ID != "i3" {
		t.Fatalf("late item = %+v err=%v", late, err)
	}
	itemsAfter, err := s.Items("r", g1.ID)
	if err != nil || len(itemsAfter) != 3 || itemsAfter[0].ID != "i1" || itemsAfter[1].ID != "i2" {
		t.Fatalf("items after late add = %v err=%v", itemsAfter, err)
	}
}
