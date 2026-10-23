package goals

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestSchemaThreeRetiresOnlyExplicitServiceGoals(t *testing.T) {
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s := &Store{Root: filepath.Join(t.TempDir(), "goals"), Now: func() time.Time { return at }}
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(goalsSchema + itemsSchema + `;
 ALTER TABLE goal_rooms ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
 INSERT INTO meta(key,value) VALUES ('goals.schema','3'),('goal_items.schema','1'),('goals.resting','1'),('goals.revision','7');
 INSERT INTO goal_rooms(room,body,revision) VALUES ('r','{}',7);
 INSERT INTO goals(room,id,state,body) VALUES
 ('r','g1','active','{"id":"g1","state":"active","service":true,"futureGoal":1}'),
 ('r','g2','dropped','{"id":"g2","state":"dropped","service":true,"done":{"at":"2026-01-01T00:00:00Z","by":"x","summary":"old"}}'),
 ('r','g3','active','{"id":"g3","state":"active","service":false,"futureGoal":3}'),
 ('r','g4','active','{"id":"g4","state":"active"}');`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(db); err != nil {
		t.Fatal(err)
	}
	body := func(id string) (string, string) {
		var state, b string
		if err := db.QueryRow("SELECT state,body FROM goals WHERE id=?", id).Scan(&state, &b); err != nil {
			t.Fatal(err)
		}
		return state, b
	}
	if st, b := body("g1"); st != "dropped" || strings.Contains(b, `"service":`) || !strings.Contains(b, `"futureGoal":1`) || !strings.Contains(b, `"state":"dropped"`) || !strings.Contains(b, "2026-10-22T12:00:00Z") {
		t.Fatal(st, b)
	}
	if st, b := body("g2"); st != "dropped" || strings.Contains(b, `"service":`) || !strings.Contains(b, `"summary":"old"`) {
		t.Fatal(st, b)
	}
	if st, b := body("g3"); st != "active" || strings.Contains(b, `"service":`) || !strings.Contains(b, `"futureGoal":3`) {
		t.Fatal(st, b)
	}
	if st, _ := body("g4"); st != "active" {
		t.Fatal(st)
	}
	after, _ := s.Revision()
	if after != "8" {
		t.Fatal("revision", after)
	}
	if err := s.migrate(db); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Revision(); again != after {
		t.Fatal("replay advanced revision")
	}
}
