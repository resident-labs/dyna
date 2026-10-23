package watch

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestMergeNoteStoresQuietPostAndDecisionHistory(t *testing.T) {
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: &Store{Root: t.TempDir()}, Notify: notify.New(rooms, subagent.NewManager())}
	t.Setenv("DYNA_MODE", "root")
	t.Setenv("DYNA_SESSION", "ses_root")
	t.Setenv("DYNA_ROOM_ID", rm.ID)
	t.Setenv("DYNA_ROOM", rm.Name)
	line := "Return #17; TestQueue failed in run 81."
	for range 2 {
		if err := s.MergeControl(t.Context(), rm.ID, "note", []string{line}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	posts, _, err := rooms.ReadAll(rm.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var decisions []room.Message
	for _, post := range posts {
		if post.Kind == "merge-decision" {
			decisions = append(decisions, post)
			if post.Wake || room.WakesRoot(post, rm.Name) || !post.Addressed(rm.Name) || post.Text != line {
				t.Fatalf("decision did not stay quiet: %+v", post)
			}
		}
	}
	history, err := s.RoomMergeHistory(rm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 || len(history) != 2 || history[0].Kind != "decision" || history[0].Detail != line || history[0].Branch != decisions[1].ID {
		t.Fatalf("posts=%+v history=%+v", decisions, history)
	}
	other, err := rooms.Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if history, err := s.RoomMergeHistory(other.ID); err != nil || len(history) != 0 {
		t.Fatalf("decision leaked to another room: %+v %v", history, err)
	}
}

func TestMergeNoteRequiresStoredRootAndOwnStampedRoom(t *testing.T) {
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	manager := subagent.NewManager()
	manager.Store = &subagent.Store{Root: t.TempDir()}
	s := &Service{Store: &Store{Root: t.TempDir()}, Notify: notify.New(rooms, manager)}
	id := "ses_20261017-000001-aaaaaaaa"
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_SESSION", id)
	t.Setenv("DYNA_ROOM", rm.Name)
	for _, tc := range []struct {
		name, role, storedRoom, stamp string
		closed, allowed               bool
	}{
		{"ordinary session", "", rm.ID, rm.ID, false, false},
		{"name alone", "reviewer", rm.ID, rm.ID, false, false},
		{"stored root", subagent.RoleRoot, rm.ID, rm.ID, false, true},
		{"wrong stored room", subagent.RoleRoot, "other", rm.ID, false, false},
		{"wrong stamp", subagent.RoleRoot, rm.ID, "other", false, false},
		{"closed root", subagent.RoleRoot, rm.ID, rm.ID, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := subagent.Meta{ID: id, Name: "root", Role: tc.role, RoomID: tc.storedRoom, Room: rm.Name}
			if tc.closed {
				meta.Closed = time.Now()
			}
			recordtest.Write(t, manager.Store.Root, id, meta)
			t.Setenv("DYNA_ROOM_ID", tc.stamp)
			err := s.mergeNote(context.Background(), rm.ID, []string{"Retry #17; TestQueue passes on main."}, nil)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
}

func TestMergeNoteRejectsInvalidLines(t *testing.T) {
	for _, args := range [][]string{nil, {""}, {"  "}, {"one", "two"}, {"one\ntwo"}, {"one\rtwo"}} {
		if err := (&Service{}).mergeNote(context.Background(), "unused", args, nil); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	if err := (&Service{}).mergeNote(context.Background(), "unused", []string{"line"}, []string{"17"}); err == nil {
		t.Fatal("accepted retry exclusions")
	}
}
