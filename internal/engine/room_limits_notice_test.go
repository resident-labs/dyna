package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// The degrade notice is one typed machine event per room and cause: it draws
// a card, never raw text, and a repeat post changes nothing.
func TestRoomLimitsNoticePostsOneTypedEvent(t *testing.T) {
	root := t.TempDir()
	s := notify.New(&room.Store{Root: filepath.Join(root, "rooms")}, &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}})
	rec, err := s.Rooms.Create(room.CreateInput{Name: "limits"})
	if err != nil {
		t.Fatal(err)
	}
	reason := "systemd did not delegate the memory controller to this user"
	for i := 0; i < 2; i++ {
		if err := postRoomLimitsNotice(context.Background(), s, rec.ID, reason); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}
	messages, _, err := s.Rooms.Log(rec, room.LogOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	typed := 0
	for _, m := range messages {
		if m.Kind == string(room.FamilyMachine) && m.Notice.Kind == string(room.TypeLimitsOff) {
			typed++
			if !strings.Contains(m.Text, reason) {
				t.Fatalf("notice text %q does not name the reason %q", m.Text, reason)
			}
		}
	}
	if typed != 1 {
		t.Fatalf("%d limits-off posts, want exactly one", typed)
	}
}
