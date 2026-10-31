package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTurnActivity(t *testing.T) {
	s, room, session, ctx := setup(t)
	connectAgent(ctx, t, s, session)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	turn := contract.TurnID("turn_" + session)
	other, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "other", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	connectAgent(ctx, t, s, other.Session)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	// room.list names the newest label among the room's working sessions.
	latest := func() *contract.RoomActivity {
		t.Helper()
		rooms, e := s.RoomList(ctx, owner, contract.RoomListRequest{})
		if e != nil {
			t.Fatal(e)
		}
		for _, item := range rooms.Rooms {
			if item.ID == room {
				return item.Latest
			}
		}
		t.Fatalf("room %s is not listed", room)
		return nil
	}
	if got := latest(); got != nil {
		t.Fatalf("latest before any activity: %+v", got)
	}
	list, e := s.SessionList(ctx, owner, contract.SessionListRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	events := ownerStream(ctx, t, s, room, list.Seq)
	ownerEvents := ownerStream(ctx, t, s, "", list.Seq)
	for _, label := range []string{"\xff", "a\nb", "a\rb", strings.Repeat("x", 81)} {
		if _, e := s.TurnActivity(ctx, caller, contract.TurnActivityRequest{Turn: turn, Label: label}); e == nil || e.Code != contract.ErrInvalid {
			t.Fatalf("label %q: %v", label, e)
		}
	}
	for _, label := range []string{"first", "latest"} {
		if _, e := s.TurnActivity(ctx, caller, contract.TurnActivityRequest{Turn: turn, Label: label}); e != nil {
			t.Fatal(e)
		}
		for _, stream := range []<-chan contract.Event{events, ownerEvents} {
			if event := <-stream; event.Seq != 0 || event.Body.(contract.TurnActivity).Label != label {
				t.Fatalf("activity event: %+v", event)
			}
		}
	}
	list, e = s.SessionList(ctx, owner, contract.SessionListRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range list.Sessions {
		if item.ID == session && item.Activity != "latest" {
			t.Fatalf("activity: %+v", item)
		}
	}
	post, e := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "after activity"})
	if e != nil {
		t.Fatal(e)
	}
	if event := <-events; event.Seq != post.Seq {
		t.Fatalf("resume point moved: %+v", event)
	}
	if got := latest(); got == nil || *got != (contract.RoomActivity{Session: session, Name: record.Name, Label: "latest"}) {
		t.Fatalf("latest: %+v", got)
	}
	if _, e := s.TurnActivity(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: other.Session}, contract.TurnActivityRequest{Turn: contract.TurnID("turn_" + other.Session), Label: "newer"}); e != nil {
		t.Fatal(e)
	}
	if got := latest(); got == nil || got.Name != "other" || got.Label != "newer" {
		t.Fatalf("newer label: %+v", got)
	}
	if _, e := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted}); e != nil {
		t.Fatal(e)
	}
	list, e = s.SessionList(ctx, owner, contract.SessionListRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range list.Sessions {
		if item.ID == session && item.Activity != "" {
			t.Fatalf("ended activity: %+v", item)
		}
	}
}
