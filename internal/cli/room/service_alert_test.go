package room

import (
	"strings"
	"testing"

	"dyna-agent/internal/notify"
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
	if err != nil || len(posts) != 1 || posts[0].Kind != notify.ServiceAlertKind {
		t.Fatalf("alert flag: %+v %v", posts, err)
	}
}

func TestPostNeedsRootFlagIsGone(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	if _, err := execCmd(t, PostCmd(), "@work", "--needs-root", "Choose the merge order."); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("a stale --needs-root must fail as an unknown flag, got %v", err)
	}
}
