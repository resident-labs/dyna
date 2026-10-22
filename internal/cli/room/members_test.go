package room

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestRoomMembersShowsLegacyDoneAsDormantUntilDismissed(t *testing.T) {
	clearStamp(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	work, err := room.DefaultStore().Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	store := subagent.DefaultStore()
	meta := subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", RoomID: work.ID, Room: work.Name, Name: "finished-lead", Closed: time.Now()}
	// An older dyna wrote finished top-level sessions as done and closed.
	b, _ := json.Marshal(meta)
	b = append(b[:len(b)-1], []byte(`,"done":true}`)...)
	recordtest.WriteRaw(t, store.Root, meta.ID, b)
	out, err := execCmd(t, RoomCmd(), "members", "work")
	if err != nil || !strings.Contains(out, "work:finished-lead (dormant,") {
		t.Fatalf("legacy done member is not dormant: %q %v", out, err)
	}
	if _, err := subagent.NewManager().CloseWithOptions(context.Background(), meta.ID, subagent.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	out, err = execCmd(t, RoomCmd(), "members", "work")
	if err != nil || strings.Contains(out, "finished-lead") {
		t.Fatalf("dismissed member visible: %q %v", out, err)
	}
}
