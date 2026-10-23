package roomsvc

import (
	"context"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestRootSessionOfAnExternalRootHasPostsAndNoTurns(t *testing.T) {
	e := newEnv(t)
	rec := e.room("beta")
	agent := e.start(rec, "agent", nil)
	store := e.svc.Notify.Rooms
	if _, _, err := store.SetSeat(context.Background(), rec.ID, room.Seat{External: true, Harness: "claude"}, nil); err != nil {
		t.Fatal(err)
	}
	owner := notify.Sender{Kind: notify.SenderOwner, Room: rec.Name, Human: true}
	external := notify.Sender{Kind: notify.SenderOwner, Room: rec.Name} // an external root posts as the room, not human
	session := func(info subagent.Info) notify.Sender {
		return notify.Sender{Kind: notify.SenderSession, Room: rec.Name, Session: info}
	}
	want := map[string]bool{}
	post := func(kept bool, in notify.PostInput) {
		t.Helper()
		in.NoWake = true
		res, err := e.svc.Notify.Post(e.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		want[res.Message.ID] = kept
	}
	post(true, notify.PostInput{Sender: owner, Mentions: []string{"@root"}, Text: "Owner asks root"})
	post(true, notify.PostInput{Sender: external, Mentions: []string{"@root"}, Text: "Root answers"})
	post(true, notify.PostInput{Sender: session(agent), Mentions: []string{"@root"}, Text: "Agent reports to root"})
	post(true, notify.PostInput{Sender: external, Mentions: []string{"@agent"}, Text: "Root steers agent"})
	post(false, notify.PostInput{Sender: owner, Mentions: []string{"@agent"}, Text: "Owner prompts the agent"})
	unrelated, err := store.Post(rec.ID, room.PostInput{From: agent.Address(), To: []string{rec.Name + ":other"}, Text: "Unrelated to root"})
	if err != nil {
		t.Fatal(err)
	}
	want[unrelated.ID] = false
	post(false, notify.PostInput{Event: room.PREvent{Type: room.PRReady, Label: "PR #1", Location: "github.com/o/r#1", ChangeID: "github.com/o/r#1", Number: 1}, Sender: external, Mentions: []string{"@root"}})
	keeper, err := store.Post(rec.ID, room.PostInput{From: "goal-keeper", To: []string{rec.Name}, Text: "Keeper nudge"})
	if err != nil {
		t.Fatal(err)
	}
	want[keeper.ID] = true
	sess, err := e.svc.RootSession(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Address != rec.Name+":root" || sess.Member.Role != "root" || sess.Member.Seat == nil || !sess.Member.Seat.External || sess.Member.Seat.Harness != "claude" {
		t.Fatalf("session: %+v", sess)
	}
	if len(sess.Turns) != 0 {
		t.Fatalf("an external root has no turns: %+v", sess.Turns)
	}
	got := map[string]bool{}
	for _, p := range sess.Posts {
		got[p.ID] = true
	}
	for id, kept := range want {
		if got[id] != kept {
			t.Errorf("post %s: in session = %v, want %v", id, got[id], kept)
		}
	}
}

func TestRootSessionMediaFollowsTheSameConversation(t *testing.T) {
	e := newEnv(t)
	rec := e.room("delta")
	agent := e.start(rec, "agent", nil)
	send := func(from subagent.Info, to, name string) string {
		res, err := e.svc.Notify.Post(e.ctx, notify.PostInput{NoWake: true, Sender: notify.Sender{Kind: notify.SenderSession, Room: rec.Name, Session: from}, Mentions: []string{to}, Text: "File " + name, Files: []notify.File{{Name: name, Data: []byte("data")}}})
		if err != nil {
			t.Fatal(err)
		}
		return res.Message.ID
	}
	toRoot := send(agent, "@root", "root.txt")
	att, err := e.svc.Notify.Rooms.PutAttachment(rec.ID, "other.txt", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: agent.Address(), To: []string{rec.Name + ":other"}, Text: "Not for root", Attachments: []room.Attachment{att}}); err != nil {
		t.Fatal(err)
	}
	sess, err := e.svc.RootSession(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Media) != 1 || sess.Media[0].ID != toRoot {
		t.Fatalf("media: %+v", sess.Media)
	}
}

func TestRootSessionBeforeTheInternalRootStartsIsEmptyNotAnError(t *testing.T) {
	e := newEnv(t)
	rec := e.room("gamma")
	sess, err := e.svc.RootSession(rec.ID)
	if err != nil || sess.Address != rec.Name+":root" || sess.Member.Seat == nil || sess.Member.Seat.External || len(sess.Turns) != 0 {
		t.Fatalf("session: %+v %v", sess, err)
	}
}
