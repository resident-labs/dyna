package goals

import (
	"dyna-agent/internal/database"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestingMigrationPreservesStoredGoalAndReplaysOnce(t *testing.T) {
	s := legacyStore(t, []byte(`{"version":1,"global":{"rules":{"r1":{"text":"global"}},"future":7},"rooms":{"r":{"futureRoom":8,"notices":[{"id":"n1","futureNotice":9}],"goals":{"g1":{"id":"g1","room":"r","state":"resting","futureGoal":10}}}}}`))
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	var body, room, global, state string
	if err := db.QueryRow("SELECT state,body FROM goals WHERE id='g1'").Scan(&state, &body); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT body FROM goal_rooms WHERE room='r'").Scan(&room); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT body FROM goal_rooms WHERE room='*'").Scan(&global); err != nil {
		t.Fatal(err)
	}
	if state != "active" || !strings.Contains(body, `"futureGoal":10`) || !strings.Contains(room, `"futureNotice":9`) || !strings.Contains(global, `"future":7`) {
		t.Fatalf("raw import: state=%s goal=%s room=%s global=%s", state, body, room, global)
	}

	// Simulate a schema-3 database written before resting normalization existed.
	s = &Store{Root: filepath.Join(t.TempDir(), "goals")}
	db, err = database.Open(filepath.Dir(s.Root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(goalsSchema + itemsSchema + `;
 ALTER TABLE goal_rooms ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
 INSERT INTO meta(key,value) VALUES ('goals.schema','3'),('goal_items.schema','1'),('goals.revision','41');
 INSERT INTO goal_rooms(room,body,revision) VALUES ('r','{}',41);
 INSERT INTO goals(room,id,state,body) VALUES ('r','g1','resting','{"id":"g1","state":"resting","futureGoal":10}');
 `); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(db); err != nil {
		t.Fatal(err)
	}

	after, _ := s.Revision()
	if after != "42" {
		t.Fatal("normalization did not advance revision")
	}
	if err := db.QueryRow("SELECT state,body FROM goals WHERE id='g1'").Scan(&state, &body); err != nil {
		t.Fatal(err)
	}
	if state != "active" || !strings.Contains(body, `"state":"active"`) || !strings.Contains(body, `"futureGoal":10`) {
		t.Fatal(state, body)
	}
	if err := s.migrate(db); err != nil {
		t.Fatal(err)
	}
	replay, _ := s.Revision()
	if after != replay {
		t.Fatal("replay advanced revision")
	}
}
