package notify

import (
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func TestCloseForwardsPendingPostToRoot(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	source := e.start(rec, "source", nil)
	e.doneGoal(source)
	original, err := e.svc.Rooms.Post(rec.ID, room.PostInput{From: rec.Name, To: []string{source.Address()}, Wake: true, Text: "RESPOND: inherited"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Close(e.ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	root := e.rootSession(rec)
	e.wait(root.ID, 1)
	if got := e.turnInput(root.ID, 1); !strings.Contains(got, original.ID) {
		t.Fatalf("root input missing forwarded post %s: %s", original.ID, got)
	}
}

func TestPostToClosedChildTargetsOpenParent(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &parent)
	e.doneGoal(child)
	if _, err := e.mgr.Close(e.ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	result, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderOwner, Room: rec.Name, Human: true}, Mentions: []string{"@child"}, Text: "continue"})
	if err != nil || !reflect.DeepEqual(result.Message.To, []string{parent.Address()}) {
		t.Fatalf("post = %+v, %v", result.Message, err)
	}
}
