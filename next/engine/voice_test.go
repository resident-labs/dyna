package engine

import (
	"github.com/resident-labs/dyna/contract"
	"strings"
	"testing"
)

func TestVoiceOwnerOnlyAndSpokenFrame(t *testing.T) {
	s, room, worker, ctx := setup(t)
	session := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	if _, e := s.VoiceStatus(ctx, session, contract.VoiceStatusRequest{}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("status: %v", e)
	}
	if _, e := s.VoiceStop(ctx, session, contract.VoiceStopRequest{}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("stop: %v", e)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if status, e := s.VoiceStatus(ctx, owner, contract.VoiceStatusRequest{}); e != nil || status.Room != "" {
		t.Fatalf("idle status: %+v %v", status, e)
	}
	tree, err := s.store.RoomTree(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	root := tree.Root()
	result, e := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "Voice dispatch", Spoken: true, Home: contract.Conversation{Kind: contract.ConversationDM, Session: root}})
	if e != nil {
		t.Fatal(e)
	}
	events, err := s.store.Events(ctx, room, result.Seq-1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	post := events[0].Body.(contract.PostCreated)
	if !post.Spoken || post.Home.Session != root || post.Home.Kind != contract.ConversationDM {
		t.Fatalf("post: %+v", post)
	}
	frame, _, err := s.inputFrame(ctx, root, events[0], contract.Delivery{Surface: contract.SurfaceDM})
	if err != nil || !strings.Contains(frame, "transcript that may be unpunctuated or misheard") {
		t.Fatalf("frame: %s %v", frame, err)
	}
}
