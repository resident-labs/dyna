package state

import (
	"fmt"
	"testing"
)

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
	if _, err := mustOpen(t, root).Exec(`UPDATE pr_rows SET body='[]' WHERE room<>'room' OR closed=1`); err != nil {
		t.Fatal(err)
	}
	records, err := OpenRecordsInRoom(root, "room")
	if err != nil || len(records) != 3 {
		t.Fatalf("OpenRecordsInRoom = %d records, %v; want the room's 3 open ones", len(records), err)
	}
}

// Goal status and session links need the PRs of a few sessions, closed ones
// included. Only the sessions' records come back.
func TestReadForSessionsReturnsOnlyTheSessionsRecords(t *testing.T) {
	root := newRoot(t)
	rec := func(n int, room, state, author, reviewer string) *Record {
		key := fmt.Sprintf("github.com/acme/widgets#%d", n)
		return &Record{Key: key, Repo: "acme/widgets", Number: n, RoomID: room, State: PRState(state), Author: author, Reviewer: reviewer}
	}
	st := State{Version: 1, Records: map[string]*Record{}}
	for _, r := range []*Record{
		rec(1, "room", "open", "ann", ""),
		rec(2, "room", "merged", "bob", "ann"), // closed, reviewed by ann
		rec(3, "room", "open", "cy", "dee"),    // neither
		rec(4, "other", "open", "ann", ""),     // another room
	} {
		st.Records[r.Key] = r
	}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	got, err := ReadForSessions(root, "room", "ann")
	if err != nil || len(got.Records) != 2 || got.Records["github.com/acme/widgets#2"] == nil {
		t.Fatalf("ReadForSessions = %d records, %v; want ann's authored #1 and reviewed #2", len(got.Records), err)
	}
}
