package listchange

import (
	"encoding/json"
	"testing"
)

func TestRowChangesAndRoomMoves(t *testing.T) {
	snapshot := map[string]string{}
	rows := []Item{{RoomID: "a", ID: "item", Value: map[string]string{"text": "Before"}}}
	if events, err := Sync(snapshot, rows, true); err != nil || len(events) != 0 {
		t.Fatalf("baseline: %+v %v", events, err)
	}
	if events, err := Sync(snapshot, rows, false); err != nil || len(events) != 0 {
		t.Fatalf("unchanged: %+v %v", events, err)
	}
	rows[0].RoomID = "b"
	rows[0].Value = map[string]string{"text": "After"}
	events, err := Sync(snapshot, rows, false)
	if err != nil || len(events) != 2 || events[0].RoomID != "a" || events[0].Change.Op != "delete" || events[1].RoomID != "b" || events[1].Change.Op != "upsert" {
		t.Fatalf("move: %+v %v", events, err)
	}
	if string(events[1].Change.Item) != `{"text":"After"}` {
		t.Fatalf("changed item: %s", events[1].Change.Item)
	}
}

func TestHeadMembershipAndEncodingFailure(t *testing.T) {
	type row struct {
		ID string `json:"id"`
	}
	id := func(r row) string { return r.ID }
	snapshot := map[string]string{}
	items := Rows("test", "room", []row{{ID: "old"}}, map[string]bool{}, id)
	if _, err := Sync(snapshot, items, true); err != nil {
		t.Fatal(err)
	}
	inHead := true
	items[0].InHead = &inHead
	events, err := Sync(snapshot, items, false)
	if err != nil || len(events) != 1 || events[0].Change.InHead == nil || !*events[0].Change.InHead {
		t.Fatalf("head change: %+v %v", events, err)
	}
	before, _ := json.Marshal(snapshot)
	items[0].Value = func() {}
	if _, err := Sync(snapshot, items, false); err == nil {
		t.Fatal("accepted an item that cannot be encoded")
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("failed encoding changed the snapshot")
	}
	unpaged := Rows("test", "room", []row{{ID: "old"}}, nil, id)
	if unpaged[0].InHead != nil {
		t.Fatal("unpaged row carries head membership")
	}
}

func TestOneRowCanBelongToTwoIndependentListHeads(t *testing.T) {
	inHead, outside := true, false
	value := map[string]string{"key": "pr", "title": "Before"}
	rows := []Item{{Kind: "prs", RoomID: "room", ID: "pr", Value: value, InHead: &inHead}, {Kind: "changes", RoomID: "room", ID: "pr", Value: value, InHead: &outside}}
	snapshot := map[string]string{}
	if _, err := Sync(snapshot, rows, true); err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 2 {
		t.Fatalf("independent list heads kept %d rows, want 2", len(snapshot))
	}
	value["title"] = "After"
	events, err := Sync(snapshot, rows, false)
	if err != nil || len(events) != 2 {
		t.Fatalf("changed rows: %+v %v", events, err)
	}
	heads := map[string]bool{}
	for _, event := range events {
		heads[event.Kind] = *event.Change.InHead
	}
	if !heads["prs"] || heads["changes"] {
		t.Fatalf("list heads: %v", heads)
	}
	events, err = Sync(snapshot, rows[:1], false)
	if err != nil || len(events) != 1 || events[0].Kind != "changes" || events[0].Change.Op != "delete" {
		t.Fatalf("list removal: %+v %v", events, err)
	}
}

// Patch reports changes for the given rows and deletes for the gone keys
// only; every other row keeps its snapshot.
func TestPatchChangesGivenRowsOnly(t *testing.T) {
	snapshot := map[string]string{}
	rows := []Item{{Kind: "prs", RoomID: "a", ID: "1", Value: "a1"}, {Kind: "prs", RoomID: "a", ID: "2", Value: "a2"}, {Kind: "prs", RoomID: "b", ID: "3", Value: "b3"}}
	if _, err := Sync(snapshot, rows, true); err != nil {
		t.Fatal(err)
	}
	events, err := Patch(snapshot, []Item{{Kind: "prs", RoomID: "a", ID: "1", Value: "a1 changed"}}, []string{Key("prs", "a", "2"), Key("prs", "a", "missing")})
	if err != nil || len(events) != 2 || events[0].Change.Op != "upsert" || events[0].Change.ID != "1" || events[1].Change.Op != "delete" || events[1].Change.ID != "2" {
		t.Fatalf("patch: %+v %v; want an upsert of 1 and a delete of 2", events, err)
	}
	if _, kept := snapshot[Key("prs", "b", "3")]; !kept || len(snapshot) != 2 {
		t.Fatalf("snapshot %v; want room b kept and the deleted row gone", snapshot)
	}
}

// countingValue counts how often a row value is encoded.
type countingValue struct{ n *int }

func (c countingValue) MarshalJSON() ([]byte, error) { *c.n++; return []byte(`{}`), nil }

// A row whose version is unchanged is not encoded again: most rows of a
// large list are unchanged on each pass.
func TestUnchangedVersionSkipsEncoding(t *testing.T) {
	encoded := 0
	rows := []Item{{Kind: "prs", RoomID: "a", ID: "1", Value: countingValue{&encoded}, Version: "v1"}}
	snapshot := map[string]string{}
	if _, err := Sync(snapshot, rows, true); err != nil {
		t.Fatal(err)
	}
	if events, err := Sync(snapshot, rows, false); err != nil || len(events) != 0 {
		t.Fatalf("unchanged: %+v %v", events, err)
	}
	if encoded != 0 {
		t.Fatalf("encoded %d times for an unchanged version; want 0", encoded)
	}
	rows[0].Version = "v2"
	if events, err := Sync(snapshot, rows, false); err != nil || len(events) != 1 || encoded != 1 {
		t.Fatalf("changed: %+v %v, encoded %d; want one upsert, encoded once", events, err, encoded)
	}
}

// HeadChanged reads the head place that Sync stored for a versioned row.
func TestHeadChangedReadsTheSnapshot(t *testing.T) {
	inHead := true
	snapshot := map[string]string{}
	if _, err := Sync(snapshot, []Item{{Kind: "prs", RoomID: "a", ID: "1", Value: "v", Version: "v1", InHead: &inHead}}, true); err != nil {
		t.Fatal(err)
	}
	stored := snapshot[Key("prs", "a", "1")]
	if HeadChanged(stored, true) || !HeadChanged(stored, false) || !HeadChanged("", true) {
		t.Fatalf("HeadChanged on %q: want false for the stored place, true for the other place and for no snapshot", stored)
	}
}
