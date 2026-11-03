package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestRootCrossRoomPost(t *testing.T) {
	s, source, root, ctx := setupRoot(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	other, problem := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	tree, err := s.store.RoomTree(ctx, other.Room)
	if err != nil {
		t.Fatal(err)
	}
	target := tree.Root()
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	req := contract.PostRequest{Room: other.Room, Text: "hello", Mentions: []string{"root"}}
	sent, problem := s.Post(ctx, caller, req)
	if problem != nil {
		t.Fatal(problem)
	}
	events, err := s.store.Events(ctx, other.Room, sent.Seq-1, 10, []contract.EventType{"post.created"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events: %v %v", events, err)
	}
	outgoing, err := s.store.Events(ctx, source, sent.Seq-1, 10, []contract.EventType{"post.created"})
	if err != nil || len(outgoing) != 1 {
		t.Fatalf("outgoing: %v %v", outgoing, err)
	}
	copy := outgoing[0].Body.(contract.PostCreated)
	if copy.Text != "hello" || copy.Target == nil || copy.Target.Room != other.Room || copy.Target.RoomName != "other" || copy.Target.SessionName != "root" {
		t.Fatalf("outgoing post: %+v", copy)
	}
	var deliveryCount int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE seq=?", outgoing[0].Seq).Scan(&deliveryCount); err != nil || deliveryCount != 0 {
		t.Fatalf("outgoing deliveries: %d %v", deliveryCount, err)
	}
	post := events[0].Body.(contract.PostCreated)
	if post.Source == nil || post.Source.Room != source || post.Source.RoomName != "room" || post.Source.SessionName != "root" {
		t.Fatalf("source: %+v", post.Source)
	}
	var kind contract.ReplyKind
	var wake bool
	if err := s.store.Read().QueryRowContext(ctx, "SELECT reply_kind,wake FROM deliveries WHERE seq=? AND to_id=?", sent.Seq, target).Scan(&kind, &wake); err != nil || kind != contract.ReplyPost || !wake {
		t.Fatalf("delivery: %s %v %v", kind, wake, err)
	}
	frame, _, err := s.inputFrame(ctx, target, events[0], contract.Delivery{Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyPost}})
	if err != nil || !strings.Contains(frame, "From room:root") {
		t.Fatalf("frame: %s %v", frame, err)
	}
	// A pending delivery remains readable even after its sender is gone.
	events[0].From.Session = "deleted-session"
	frame, _, err = s.inputFrame(ctx, target, events[0], contract.Delivery{Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyPost}})
	if err != nil || !strings.Contains(frame, "From room:root") {
		t.Fatalf("deleted sender frame: %s %v", frame, err)
	}
	for _, bad := range []contract.PostRequest{
		{Room: other.Room, Text: "x", Mentions: []string{"parent"}},
		{Room: other.Room, Text: "x", Mentions: []string{"root"}, ReplyTo: "ev-test"},
		{Room: other.Room, Text: "x", Mentions: []string{"root"}, Home: contract.Conversation{Kind: contract.ConversationDM, Session: root}},
		{Room: other.Room, Text: "x", Mentions: []string{"root"}, VisualUpdate: "ev-test"},
		{Room: other.Room, Text: " ", Mentions: []string{"root"}},
	} {
		if _, p := s.Post(ctx, caller, bad); p == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	req.Attachments = []contract.Attachment{{Name: "x"}}
	if _, p := s.Post(ctx, caller, req); p == nil || p.Code != contract.ErrCrossRoomAttachment {
		t.Fatalf("attachment: %v", p)
	}
	var worker contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT id FROM sessions WHERE parent=? LIMIT 1", root).Scan(&worker); err != nil {
		t.Fatal(err)
	}
	req.Attachments = nil
	if _, p := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, req); p == nil || !strings.Contains(p.Message, "report the problem to your room's root with post @root") {
		t.Fatalf("worker: %v", p)
	}
}
