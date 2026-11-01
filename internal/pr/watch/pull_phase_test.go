package watch

import (
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/ghquota"
	"dyna-agent/internal/state"
)

// A fake pull read records its operation, not the ambiguous other phase.
func TestPullReadsRecordOperationPhase(t *testing.T) {
	e := newEnv(t)
	e.write()
	r := e.track()
	dir := e.s.dataDir()
	if err := ghquota.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ghquota.UseStore(state.For(db))()
	gh := e.s.github(r.RoomID)
	if _, _, err := gh.pollSnapshot(t.Context(), r, ChangePolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.reviewHeadCurrent(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	var snapshot, reviewer int
	for label, dest := range map[string]*int{"pull/watch-snapshot": &snapshot, "pull/reviewer-head": &reviewer} {
		if err := db.QueryRow("SELECT counted FROM ghquota_counts WHERE kind='endpoint-phase' AND label=?", label).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot != 1 || reviewer != 1 {
		t.Fatalf("pull phase counts = %d, %d; want 1, 1", snapshot, reviewer)
	}
}
