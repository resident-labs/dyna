package room

import (
	"path/filepath"
	"testing"
)

// A session's rows name its session ID, so the read finds the post after a
// rename; a post that named an address without binding a session is keyed by
// that address, so the read finds it while the address is still this session's.
func TestSessionDeliveriesFollowTheSessionID(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := s.Post(rec.ID, PostInput{Wake: true, From: "alpha:lead", To: []string{"alpha:old"}, SessionIDs: map[string]string{"alpha:old": ownerSession}, Text: "before the rename"})
	if err != nil {
		t.Fatal(err)
	}
	named, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha:old"}, Text: "no binding"})
	if err != nil {
		t.Fatal(err)
	}
	keys := DeliveryKeys(ownerSession, "alpha:new")
	rows, err := s.SessionDeliveries(rec.ID, keys, []string{bound.ID, named.ID})
	if err != nil {
		t.Fatal(err)
	}
	if row, ok := rows[bound.ID]; !ok || row.Mode != DeliveryWake {
		t.Fatalf("rows = %+v: the bound post left the session on its rename", rows)
	}
	if _, ok := rows[named.ID]; ok {
		t.Fatalf("rows = %+v: the post named the session's old address after the rename", rows)
	}
	// The address the post named is still the key while the session answers to
	// it, as the address text matched before the rows existed.
	rows, err = s.SessionDeliveries(rec.ID, DeliveryKeys(ownerSession, "alpha:old"), []string{named.ID})
	if err != nil {
		t.Fatal(err)
	}
	if row, ok := rows[named.ID]; !ok || row.Mode != DeliveryQuiet {
		t.Fatalf("rows = %+v, want the quiet post keyed by its address", rows)
	}
}

// A store that indexed its posts before an unbound recipient got a row gets the
// rows once: a session reads its input from them, so a post that reached it by
// its address text must not stop reaching it.
func TestPostIndexBackfillsUnboundRecipients(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.Post(rec.ID, PostInput{Wake: true, From: rec.Name, To: []string{"alpha:bea"}, Text: "no binding"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.postIndexDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DELETE FROM post_deliveries WHERE post_id='" + msg.ID + "'",
		"DELETE FROM meta WHERE key='" + postIndexRecipientsKey + "'",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if rows, err := s.SessionDeliveries(rec.ID, []string{"alpha:bea"}, []string{msg.ID}); err != nil || len(rows) != 0 {
		t.Fatalf("rows before the backfill = %+v, %v: want none", rows, err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	rows, err := s.SessionDeliveries(rec.ID, []string{"alpha:bea"}, []string{msg.ID})
	if err != nil || rows[msg.ID].Mode != DeliveryWake {
		t.Fatalf("rows after the backfill = %+v, %v: want the wake row of the named address", rows, err)
	}
	if _, _, deliveries := postIndexRows(t, s, rec.ID, msg.ID); len(deliveries) != 1 || deliveries[0].state != string(DeliveryImported) {
		t.Fatalf("deliveries = %+v, want one imported row: the index cannot tell whether it reached the session", deliveries)
	}
}
