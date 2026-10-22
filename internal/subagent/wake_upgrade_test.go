package subagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// appendLegacyWakePost writes the pre-upgrade format with no wake field.
func appendLegacyWakePost(t *testing.T, e *testEnv, info Info, kind string) string {
	t.Helper()
	id := "legacy-" + kind
	f, err := os.OpenFile(filepath.Join(e.m.rooms().Root, info.RoomID, "messages.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewEncoder(f).Encode(map[string]any{"id": id, "roomId": info.RoomID, "ts": time.Now().UnixMilli(), "from": info.Room, "to": []string{info.Address()}, "kind": kind, "text": "SLEEP:0\nRESPOND: legacy input"})
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return id
}

func TestSessionWorkerResumesLegacyPostAfterPausedUpgrade(t *testing.T) {
	e := roomControlEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	info, _ = e.wait(info.ID, 1)
	if _, err := e.m.PauseRoomSessions(e.ctx, info.RoomID); err != nil {
		t.Fatal(err)
	}
	id := appendLegacyWakePost(t, e, info, "message")
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if meta, _ := e.m.Store.ReadMeta(info.ID); meta.Turns != 1 {
		t.Fatal("legacy input escaped room pause")
	}
	if _, err := e.m.ResumeRoomSessions(e.ctx, info.RoomID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if meta, _ := e.m.Store.ReadMeta(info.ID); meta.Turns != 2 {
		t.Fatal("upgrade stranded the pending legacy post after resume")
	}
	_, turn := e.wait(info.ID, 2)
	if len(turn.Steering) != 1 || turn.Steering[0].ID != id || !turn.Steering[0].Delivered {
		t.Fatalf("legacy delivery: %+v", turn)
	}
}

func TestSessionWorkerDeliveredLegacyPostNeverWakesAgain(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	info, _ = e.wait(info.ID, 1)
	id := appendLegacyWakePost(t, e, info, "message")
	// The old poster already delivered this input before the upgrade.
	if _, err := e.m.Wake(e.ctx, info.ID, WakeOptions{MessageID: id}); err != nil {
		t.Fatal(err)
	}
	info, turn := e.wait(info.ID, 2)
	if len(turn.Steering) != 1 || !turn.Steering[0].Delivered {
		t.Fatalf("old delivery: %+v", turn)
	}
	appendLegacyWakePost(t, e, info, "instructions")
	for range 2 {
		if _, err := e.m.TickSessions(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if meta, _ := e.m.Store.ReadMeta(info.ID); meta.Turns != 2 {
		t.Fatalf("legacy history or instruction note woke turn %d", meta.Turns)
	}
}
