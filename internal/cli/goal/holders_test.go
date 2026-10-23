package goal

import (
	"dyna-agent/internal/goalsvc"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"fmt"
	"path/filepath"
	"testing"
)

func TestHolderSiblingsUsePostEligibility(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	mgr := &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}, Rooms: rooms}
	s := goalsvc.New()
	s.Notify = notify.New(rooms, mgr)
	var caller subagent.Info
	var want string
	for i, name := range []string{"caller", "task", "service", "nonresumable", "unstarted"} {
		meta := subagent.Meta{ID: fmt.Sprintf("ses_20261019-00000%d-aaaaaaaa", i+1), Name: name, RoomID: rec.ID, Room: rec.Name, Depth: 1, Resumable: true, HarnessSession: "mock-fixture", Status: subagent.StatusIdle}
		if name == "service" {
			meta.Service = true
		}
		if name == "nonresumable" {
			meta.Resumable = false
		}
		if name == "unstarted" {
			meta.HarnessSession = ""
		}
		recordtest.Write(t, mgr.Store.Root, meta.ID, meta)
		if name == "caller" {
			caller, err = mgr.Store.Inspect(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		if name == "task" {
			want = meta.ID
		}
	}
	preview, err := s.Notify.Preview(notify.Sender{Kind: notify.SenderSession, Room: rec.Name, Session: caller}, []string{"@siblings"})
	if err != nil || len(preview.Recipients) != 1 || preview.Recipients[0] != rec.Name+":task" {
		t.Fatalf("post eligibility=%+v,%v", preview, err)
	}
	holders, shared, err := s.ResolveHolders(rec.ID, caller.ID, []string{"@siblings"})
	if err != nil || shared || len(holders) != 1 || holders[0] != want {
		t.Fatalf("holders=%v, shared=%v, err=%v", holders, shared, err)
	}
	for _, tc := range []struct {
		name      string
		selectors []string
		fails     bool
	}{
		{"deduplicated", []string{"@task", "@task"}, false},
		{"qualified", []string{"@work:task"}, false},
		{"nonresumable", []string{"@nonresumable"}, true},
		{"unstarted", []string{"@unstarted"}, true},
		{"owner is not a session", []string{"@root"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			holders, _, err := s.ResolveHolders(rec.ID, caller.ID, tc.selectors)
			if tc.fails {
				if err == nil {
					t.Fatal("accepted unusable holder", holders)
				}
				return
			}
			if err != nil || len(holders) != 1 || holders[0] != want {
				t.Fatalf("holders=%v, err=%v", holders, err)
			}
		})
	}

	for _, selectors := range [][]string{{"@caller"}, {"@work:caller"}, {"@caller", "@task"}} {
		holders, _, err := s.ResolveHolders(rec.ID, caller.ID, selectors)
		if err != nil || len(holders) != len(selectors) || holders[0] != caller.ID {
			t.Errorf("explicit self %v: holders=%v, err=%v", selectors, holders, err)
		}
	}
	if _, err := s.Notify.Preview(notify.Sender{Kind: notify.SenderSession, Room: rec.Name, Session: caller}, []string{"@caller"}); err == nil {
		t.Fatal("post preview accepted a self mention")
	}

}
