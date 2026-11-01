package room

import (
	"strings"
	"testing"
)

func TestSessionClaimRowsSkipsOtherStates(t *testing.T) {
	s, rec := newEventStore(t)
	db, err := s.postIndexDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"queued", "delivered", "claimed", "cancelled", "dismissed"} {
		_, err := db.Exec(`INSERT INTO post_deliveries(room_id, post_id, session_id, mode, category, state, updated_at) VALUES(?, ?, 'session', 'quiet', '', ?, 1)`, rec.ID, state, state)
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.SessionClaimRows(rec.ID, []string{"session"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want only claimed, cancelled and dismissed", len(rows))
	}
	plan, err := db.Query(`EXPLAIN QUERY PLAN SELECT post_id FROM post_deliveries WHERE room_id=? AND session_id IN (?) AND state IN ('claimed','cancelled','dismissed')`, rec.ID, "session")
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	var details []string
	for plan.Next() {
		var id, parent, unused int
		var detail string
		if err := plan.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := plan.Err(); err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 || !strings.Contains(details[0], "post_deliveries_claims") {
		t.Fatalf("claim query plan: %v", details)
	}
}
