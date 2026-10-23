package roomsvc

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/goals"
)

func TestGoalsWindowSkipsUnreachableBodiesAndFollowsCursor(t *testing.T) {
	e := newEnv(t)
	room := e.room("work")
	store := e.svc.goalService().Store
	db, err := database.Open(filepath.Dir(store.Root))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE goals(room TEXT NOT NULL,id TEXT NOT NULL,state TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),PRIMARY KEY(room,id));
 CREATE TABLE goal_rooms(room TEXT PRIMARY KEY,body TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 0);
 INSERT INTO meta(key,value) VALUES('goals.schema','3'),('goals.revision','1')`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2036, 10, 21, 12, 0, 0, 123456789, time.UTC)
	bodies := map[string]string{}
	for _, g := range []goals.Goal{
		{ID: "open", Room: room.ID, State: "active", CreatedAt: now},
		{ID: "recent", Room: room.ID, State: "done", UpdatedAt: now.Add(-48 * time.Hour), Done: &goals.Outcome{At: now}},
		{ID: "aged", Room: room.ID, State: "done", UpdatedAt: time.Date(2000, 1, 1, 0, 0, 0, 987654321, time.FixedZone("offset", 3600))},
		{ID: "other", Room: "elsewhere", State: "active", CreatedAt: now},
	} {
		raw, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		bodies[g.ID] = string(raw)
		if _, err = db.Exec("INSERT INTO goals VALUES(?,?,?,?)", g.Room, g.ID, g.State, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err = goals.Migrate(filepath.Dir(store.Root)); err != nil {
		t.Fatal(err)
	}
	var version string
	if err = db.QueryRow("SELECT value FROM meta WHERE key='goals.schema'").Scan(&version); err != nil || version != "4" {
		t.Fatal(version, err)
	}
	rows, err := db.Query("SELECT id,body FROM goals")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, body string
		if err = rows.Scan(&id, &body); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if body != bodies[id] {
			rows.Close()
			t.Fatalf("migration changed %s body", id)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var agedKey string
	if err = db.QueryRow("SELECT sort_at FROM goals WHERE id='aged'").Scan(&agedKey); err != nil {
		t.Fatal(err)
	}
	if agedKey != "1999-12-31T23:00:00.987654321" {
		t.Fatal(agedKey)
	}
	// The bad body is outside the first page. The key remains valid.
	if _, err = db.Exec(`UPDATE goals SET body='{"id":123,"state":"done"}' WHERE id='aged'`); err != nil {
		t.Fatal(err)
	}
	first, err := e.svc.Goals(room.ID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "open" || first.Items[1].ID != "recent" || !first.Older || first.Cursor == "" {
		t.Fatal(first)
	}
	var key, body string
	if err = db.QueryRow("SELECT sort_at,body FROM goals WHERE id='recent'").Scan(&key, &body); err != nil {
		t.Fatal(err)
	}
	if key != "2036-10-21T12:00:00.123456789" || body != bodies["recent"] {
		t.Fatal(key, body)
	}
	if _, err = db.Exec("UPDATE goals SET body=? WHERE id='aged'", bodies["aged"]); err != nil {
		t.Fatal(err)
	}
	last, err := e.svc.Goals(room.ID, first.Cursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Items) != 1 || last.Items[0].ID != "aged" || last.Older || last.Cursor != "" {
		t.Fatal(last)
	}
}
