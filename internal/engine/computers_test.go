package engine

import (
	"dyna-agent/internal/testsafe"
	"testing"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
)

// The Computer tab lists root's computer, the one the CLI uses with no stamp.
func TestComputerHostListsRootComputer(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	rooms := roomsvc.New(nil)
	rec, err := rooms.Notify.Rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := newComputerHost(rooms).Computers(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.Session == computers.ActorID("", rec.ID) && c.Name == "root" {
			return
		}
	}
	t.Fatalf("root computer missing: %+v", list)
}
