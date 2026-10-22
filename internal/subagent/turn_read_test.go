package subagent

import (
	"os"
	"slices"
	"testing"
)

// A read never writes an older turn over the row. Here a steer's write has
// committed while the turn's rollback copy is an older one that looks
// foreign, as when another binary's copy lands before the writer's own copy.
// A plain read takes the row as it is, so the steer survives, and the post
// is not delivered a second time.
func TestReadNeverWritesAnOlderTurnOverTheRow(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	store := e.m.store()
	path, err := store.turnPath(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTurn(info.ID, 1, func(t *Turn) {
		t.Steering = append(t.Steering, Steer{ID: "msg_kept", TS: 1, Message: "hello", Delivered: true})
	}); err != nil {
		t.Fatal(err)
	}
	// The older copy lands as a new file, so the stamp does not own it.
	tmp := path + ".older"
	if err := os.WriteFile(tmp, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		turn, err := store.ReadTurn(info.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(turn.Steering, func(s Steer) bool { return s.ID == "msg_kept" }) {
			t.Fatalf("read turn has steering %+v; want the committed steer msg_kept", turn.Steering)
		}
	}
}
