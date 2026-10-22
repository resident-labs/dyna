package room

import (
	"encoding/json"
	"testing"

	"dyna-agent/internal/room"
)

func TestPostAlertFlagStoresExplicitAlert(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = execCmd(t, PostCmd(), "@work", "--alert", "A dependency is unavailable."); err != nil {
		t.Fatal(err)
	}
	posts, err := rooms.Messages(rec.ID)
	if err != nil || len(posts) != 1 || posts[0].Kind != "alert" {
		t.Fatalf("alert flag: %+v %v", posts, err)
	}
}

func TestPostNeedsRootFlagPersistsWakeIntent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = execCmd(t, PostCmd(), "@work", "--needs-root", "Choose the merge order."); err != nil {
		t.Fatal(err)
	}
	posts, err := rooms.Messages(rec.ID)
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts=%+v %v", posts, err)
	}
	data, err := json.Marshal(posts[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		NeedsRoot bool `json:"needsRoot"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if !wire.NeedsRoot || !room.WakesRoot(posts[0], rec.Name) {
		t.Fatalf("missing root wake intent: %s", data)
	}
}
