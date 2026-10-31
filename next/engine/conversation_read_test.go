package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestConversationRead(t *testing.T) {
	s, room, session, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	rooms, e := s.RoomList(ctx, owner, contract.RoomListRequest{})
	if e != nil {
		t.Fatal(e)
	}
	streams := []<-chan contract.Event{ownerStream(ctx, t, s, room, rooms.Seq), ownerStream(ctx, t, s, "", rooms.Seq)}
	for _, conversation := range []contract.Conversation{{Kind: contract.ConversationFeed}, {Kind: contract.ConversationDM, Session: session}} {
		for _, seq := range []int64{10, 7} {
			req := contract.ConversationReadRequest{Room: room, Conversation: conversation, Seq: seq}
			if _, e := s.ConversationRead(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, req); e == nil || e.Code != contract.ErrForbidden {
				t.Fatalf("session caller: %v", e)
			}
			if _, e := s.ConversationRead(ctx, owner, req); e != nil {
				t.Fatal(e)
			}
			for _, stream := range streams {
				event := <-stream
				body, ok := event.Body.(contract.ConversationRead)
				if !ok || event.Seq != 0 || body.Seq != 10 || body.Conversation != conversation {
					t.Fatalf("event: %+v", event)
				}
			}
		}
	}
	rooms, e = s.RoomList(ctx, owner, contract.RoomListRequest{})
	if e != nil || len(rooms.Rooms) != 1 || rooms.Rooms[0].LastRead != 10 {
		t.Fatalf("rooms: %+v %v", rooms, e)
	}
	sessions, e := s.SessionList(ctx, owner, contract.SessionListRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, item := range sessions.Sessions {
		if item.ID == session && item.LastRead == 10 {
			found = true
		}
	}
	if !found {
		t.Fatalf("sessions: %+v %v", sessions, e)
	}
}
