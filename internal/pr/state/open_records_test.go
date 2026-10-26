package state

import "testing"

// A room's issue list needs only that room's open PRs. It must not decode the
// rest of the store: a snapshot of every record is about 17 MB and took 1-5 s
// in each CLI process.
func TestOpenRecordsInRoomReadsOnlyThatRoomsUnclosedRows(t *testing.T) {
	root := newRoot(t)
	st := bigState(3) // three open records in room "room"
	st.Records["github.com/acme/widgets#9"] = &Record{Key: "github.com/acme/widgets#9", Repo: "acme/widgets", Number: 9, RoomID: "other", State: "open"}
	st.Records["github.com/acme/widgets#10"] = &Record{Key: "github.com/acme/widgets#10", Repo: "acme/widgets", Number: 10, RoomID: "room", State: "merged"}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	// Rows outside the request would fail a full decode.
	if _, err := mustOpen(t, root).Exec(`UPDATE pr_records SET body='[]' WHERE room<>'room' OR closed=1`); err != nil {
		t.Fatal(err)
	}
	records, err := OpenRecordsInRoom(root, "room")
	if err != nil || len(records) != 3 {
		t.Fatalf("OpenRecordsInRoom = %d records, %v; want the room's 3 open ones", len(records), err)
	}
}
