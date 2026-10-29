package state

import "testing"

// A member scan asks for the same sessions' PRs many times. Only a PR change
// may make the next call read the rows again.
func TestReadForSessionsReadsPRRowsOnlyAfterARevisionChange(t *testing.T) {
	root := newRoot(t)
	st := State{Version: 1, Records: map[string]*Record{}}
	st.Records["github.com/acme/widgets#1"] = &Record{Key: "github.com/acme/widgets#1", Repo: "acme/widgets", Number: 1, RoomID: "room", State: "open", Author: "ann"}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	reads := 0
	onRowsRead = func() { reads++ }
	t.Cleanup(func() { onRowsRead = func() {} })

	for range 2 {
		got, err := ReadForSessions(root, "room", "ann")
		if err != nil || len(got.Records) != 1 {
			t.Fatalf("ReadForSessions = %d records, %v; want ann's PR", len(got.Records), err)
		}
	}
	if reads != 0 {
		t.Fatalf("reads with no PR change = %d; want 0", reads)
	}
	if _, err := mustOpen(t, root).Exec("UPDATE pr_state SET revision=revision+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := ReadForSessions(root, "room", "ann"); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Fatalf("reads after one PR change = %d; want 1", reads)
	}
	fresh, err := ReadForSessionsFresh(root, "room", "ann")
	if err != nil || len(fresh.Records) != 1 || fresh.Records["github.com/acme/widgets#1"] == nil {
		t.Fatalf("ReadForSessionsFresh = %d records, %v; want ann's PR", len(fresh.Records), err)
	}
}

// A short-lived CLI process starts with no snapshot. It must filter the rows in
// SQLite and not decode every PR row to build one.
func TestReadForSessionsColdProcessDoesNotLoadSnapshot(t *testing.T) {
	root := newRoot(t)
	st := State{Version: 1, Records: map[string]*Record{}}
	st.Records["github.com/acme/widgets#1"] = &Record{Key: "github.com/acme/widgets#1", Repo: "acme/widgets", Number: 1, RoomID: "room", State: "open", Author: "ann"}
	st.Records["github.com/acme/widgets#2"] = &Record{Key: "github.com/acme/widgets#2", Repo: "acme/widgets", Number: 2, RoomID: "room", State: "open", Author: "bob"}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	forgetSnapshot()
	reads := 0
	onRowsRead = func() { reads++ }
	t.Cleanup(func() { onRowsRead = func() {} })

	got, err := ReadForSessions(root, "room", "ann")
	if err != nil || len(got.Records) != 1 || got.Records["github.com/acme/widgets#1"] == nil {
		t.Fatalf("ReadForSessions = %d records, %v; want ann's PR", len(got.Records), err)
	}
	if reads != 0 {
		t.Fatalf("PR row reads in a cold process = %d; want 0", reads)
	}
}

// The cold read must return the cold parts of a record too, the same as a
// read from a warm snapshot.
func TestReadForSessionsColdRecordKeepsItsParts(t *testing.T) {
	root := newRoot(t)
	const key = "github.com/acme/widgets#1"
	st := State{Version: 1, Records: map[string]*Record{}}
	st.Records[key] = &Record{Key: key, Repo: "acme/widgets", Number: 1, RoomID: "room", State: "open", Author: "ann", Summary: "lint failed", Notices: []Notice{{}}}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	forgetSnapshot()
	cold, err := ReadForSessions(root, "room", "ann")
	if err != nil || cold.Records[key] == nil {
		t.Fatalf("cold ReadForSessions = %d records, %v; want ann's PR", len(cold.Records), err)
	}
	if _, err := Read(root); err != nil {
		t.Fatal(err)
	}
	warm, err := ReadForSessions(root, "room", "ann")
	if err != nil || warm.Records[key] == nil {
		t.Fatalf("warm ReadForSessions = %d records, %v; want ann's PR", len(warm.Records), err)
	}
	if cold.Records[key].Summary != warm.Records[key].Summary || len(cold.Records[key].Notices) != len(warm.Records[key].Notices) {
		t.Fatalf("cold record %q with %d notices; warm record %q with %d notices", cold.Records[key].Summary, len(cold.Records[key].Notices), warm.Records[key].Summary, len(warm.Records[key].Notices))
	}
}
