package store

import (
	"context"
	"strings"
	"testing"
)

// A row left behind that references one deleteRoom removes fails the transaction on every retry, which holds the room
// for good. A reference can come from a table with no room or session column, so this walks the foreign keys themselves.
func TestDeleteRoomClearsEveryReferenceToWhatItRemoves(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	statements := strings.Join(deleteRoom, "\n")
	rows, err := s.Read().QueryContext(context.Background(), `SELECT m.name,f."table" FROM sqlite_master m, pragma_foreign_key_list(m.name) f WHERE m.type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		var table, target string
		if err := rows.Scan(&table, &target); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(statements, "DELETE FROM "+target+" ") {
			continue
		}
		found++
		if !strings.Contains(statements, "DELETE FROM "+table+" ") {
			t.Errorf("%s references %s, which a room delete removes, and deleteRoom never clears it", table, target)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("the walk found no references")
	}
}
