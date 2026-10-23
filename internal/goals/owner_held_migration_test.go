package goals

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

// An old build left some goals with the owner. The migration marks those open
// goals once, so root sees that only it can move them, and leaves every other
// goal alone.
func TestOwnerHeldMigrationMarksGoalsLeftWithTheOwner(t *testing.T) {
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s := &Store{Root: filepath.Join(t.TempDir(), "goals"), Now: func() time.Time { return at }}
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(goalsSchema + itemsSchema + `;
 ALTER TABLE goals ADD COLUMN sort_at TEXT NOT NULL DEFAULT '';
 ALTER TABLE goal_rooms ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
 INSERT INTO meta(key,value) VALUES ('goals.schema','4'),('goal_items.schema','1'),('goals.resting','1'),('goals.service','1'),('goals.revision','7');
 INSERT INTO goal_rooms(room,body,revision) VALUES ('r','{}',7);
 INSERT INTO goals(room,id,state,body) VALUES
 ('r','g1','active','{"id":"g1","state":"active","setter":"owner","holders":[]}'),
 ('r','g2','active','{"id":"g2","state":"active","setter":"owner"}'),
 ('r','g3','active','{"id":"g3","state":"active","setter":"owner","holders":["ses_20261023-120000-000000000001"]}'),
 ('r','g4','active','{"id":"g4","state":"active","setter":"ses_20261023-120000-000000000002","holders":[]}'),
 ('r','g5','done','{"id":"g5","state":"done","setter":"owner","holders":[]}'),
 ('r','g6','active','{"id":"g6","state":"active","setter":"owner","shared":true,"holders":[]}')`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(db); err != nil {
		t.Fatal(err)
	}
	marked := func(id string) bool {
		var body string
		if err := db.QueryRow("SELECT body FROM goals WHERE id=?", id).Scan(&body); err != nil {
			t.Fatal(err)
		}
		var g Goal
		if err := json.Unmarshal([]byte(body), &g); err != nil {
			t.Fatal(err)
		}
		return g.NeedsRoot()
	}
	for _, id := range []string{"g1", "g2"} {
		if !marked(id) {
			t.Fatalf("%s left with the owner was not marked for root", id)
		}
	}
	for _, id := range []string{"g3", "g4", "g5", "g6"} {
		if marked(id) {
			t.Fatalf("%s must not be marked for root", id)
		}
	}
	after, err := s.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if after != "8" {
		t.Fatalf("revision after the migration = %s; want one move", after)
	}
	if err := s.migrate(db); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Revision(); again != after {
		t.Fatal("replay advanced the revision")
	}
}

// Handing a legacy owner-held goal to an agent settles the mark.
func TestAssigningALegacyGoalClearsNeedsRoot(t *testing.T) {
	s := testStore(t)
	g, err := s.Add("r", "owner", Input{Text: "Kept work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *State) error {
		st.Rooms["r"].Goals[g.ID].LegacyOwnerHeld = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("r", g.ID); err != nil || !got.NeedsRoot() {
		t.Fatalf("marked goal = %+v, %v", got, err)
	}
	holders := []string{"agent"}
	got, err := s.Edit("r", g.ID, "owner", "", &holders, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.NeedsRoot() || !got.HeldBy("agent") {
		t.Fatalf("goal after the handover = %+v", got)
	}
}
