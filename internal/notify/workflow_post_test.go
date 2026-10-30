package notify

import (
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func TestWorkflowPostReservationRecoversAndDeduplicates(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	key := strings.Repeat("a", 64) + "-0"
	reservation, err := e.svc.Rooms.ReserveWorkflowPost(e.ctx, key, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := PostInput{WorkflowKey: key, Sender: Sender{Kind: SenderScript, Room: rec.Name, RunID: "wf_original"}, Mentions: []string{"@" + rec.Name}, Text: "one post"}
	// The first attempt stopped immediately after reservation.
	first, err := e.svc.Post(e.ctx, input)
	if err != nil || first.Message.ID != reservation.PostID {
		t.Fatalf("recovery = %+v, %v; reserved ID %s", first, err, reservation.PostID)
	}
	// A later run in the same lineage sends the same step.
	input.Sender.RunID = "wf_resumed"
	again, err := e.svc.Post(e.ctx, input)
	if err != nil || again.Message.ID != first.Message.ID || len(again.Deliveries) != 0 {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	posts, err := e.svc.Rooms.Messages(rec.ID)
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts = %+v, %v; want one", posts, err)
	}
}

func TestWorkflowPostRecoversAfterAppendBeforeCompletion(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	key := strings.Repeat("b", 64) + "-0"
	reservation, err := e.svc.Rooms.ReserveWorkflowPost(e.ctx, key, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Rooms.Post(rec.ID, room.PostInput{ID: reservation.PostID, From: "run:wf_original", To: []string{rec.Name}, Text: "one post"})
	if err != nil {
		t.Fatal(err)
	}
	// The first process stopped after the append and before marking the reservation.
	result, err := e.svc.Post(e.ctx, PostInput{WorkflowKey: key, Sender: Sender{Kind: SenderScript, Room: rec.Name, RunID: "wf_resumed"}, Mentions: []string{"@" + rec.Name}, Text: "one post"})
	if err != nil || result.Message.ID != reservation.PostID || len(result.Deliveries) != 0 {
		t.Fatalf("recovery = %+v, %v", result, err)
	}
	after, err := e.svc.Rooms.ReserveWorkflowPost(e.ctx, key, rec.ID)
	if err != nil || !after.Delivered {
		t.Fatalf("reservation = %+v, %v", after, err)
	}
	posts, err := e.svc.Rooms.Messages(rec.ID)
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts = %+v, %v; want one", posts, err)
	}
}
