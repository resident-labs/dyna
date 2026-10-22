package notify

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestNewPairsStores(t *testing.T) {
	storetest.Isolate(t)
	for _, tc := range []string{"rooms", "sessions", "override"} {
		t.Run(tc, func(t *testing.T) {
			root := t.TempDir()
			rooms := &room.Store{Root: filepath.Join(root, "rooms")}
			mgr := &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}}
			switch tc {
			case "rooms":
				mgr = nil
			case "sessions":
				rooms = nil
			case "override":
				mgr.Rooms = &room.Store{Root: filepath.Join(t.TempDir(), "rooms")}
			}
			svc := New(rooms, mgr)
			if svc.Rooms.Root != filepath.Join(root, "rooms") || svc.Sessions.Store.Root != filepath.Join(root, "sessions") {
				t.Fatalf("unpaired roots: rooms=%s sessions=%s", svc.Rooms.Root, svc.Sessions.Store.Root)
			}
			if svc.Sessions.RoomStore() != svc.Rooms {
				t.Fatal("routing and execution use different room stores")
			}
			if svc.Sessions.Store.ReportRepaired == nil {
				t.Fatal("recovery reporting is not wired")
			}
		})
	}
}

func TestNewPreservesRecoveryOverrides(t *testing.T) {
	repaired := false
	store := &subagent.Store{
		Root:           filepath.Join(t.TempDir(), "sessions"),
		ReportRepaired: func(*subagent.Store, string, int) { repaired = true },
	}
	svc := New(nil, &subagent.Manager{Store: store})
	svc.Sessions.Store.ReportRepaired(store, "unused", 1)
	if !repaired {
		t.Fatal("constructor replaced explicit callbacks")
	}
}
