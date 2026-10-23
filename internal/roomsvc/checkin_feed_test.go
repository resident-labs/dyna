package roomsvc

import (
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// A parent check-in has one home: the parent's DM. It is not part of the room's
// own conversation, so the room feed does not list it. A check-in the owner is
// a recipient of stays in the room feed.
func TestCheckinStaysInTheParentDMNotTheRoomFeed(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	const parentID = "ses_20261023-120000-abcdef01"
	recordtest.Write(t, e.mgr.Store.Root, parentID, subagent.Meta{
		ID: parentID, Name: "lead", RoomID: r.ID, Room: r.Name, Status: subagent.StatusIdle, Created: time.Now(),
	})
	address := r.Name + ":lead"
	post := func(to string, window int64) room.Message {
		t.Helper()
		ev := room.CheckinEvent{
			Parent: parentID, WindowStart: window, Counts: room.CheckinCounts{Idle: 1},
			Children: []room.CheckinChild{{Name: "kid", State: room.CheckinIdle, Age: "5m", Goal: "g1: work", PR: "no open PRs"}},
		}
		msg, err := e.svc.rooms().PostEventInput(room.PostEventInput{
			RoomID: r.ID, Event: ev, Producer: room.ProducerParentCheckin,
			To: []string{to}, SessionIDs: map[string]string{address: parentID},
		})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	parent := post(address, 1)
	owner := post(r.Name, 2)

	feed, err := e.svc.FeedTail(r.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, p := range feed.Posts {
		listed[p.ID] = true
	}
	if listed[parent.ID] {
		t.Fatalf("the room feed lists the parent's check-in: %+v", feed.Posts)
	}
	if !listed[owner.ID] {
		t.Fatalf("the room feed dropped the check-in the owner received: %+v", feed.Posts)
	}

	dm, err := e.svc.Transcript(parentID)
	if err != nil {
		t.Fatal(err)
	}
	var found *room.Message
	for i, p := range dm.Posts {
		if p.ID == parent.ID {
			found = &dm.Posts[i].Message
		}
	}
	if found == nil {
		t.Fatalf("the parent's DM does not list its check-in: %+v", dm.Posts)
	}
	if found.From != room.EngineSender || found.Notice.Producer != room.ProducerParentCheckin {
		t.Fatalf("check-in sender = %q, producer = %q", found.From, found.Notice.Producer)
	}
	if ev, ok := found.Checkin(); !ok || ev.Parent != parentID {
		t.Fatalf("check-in payload = %+v, %v", ev, ok)
	}
}

// A check-in for the internal root goes to the seat's DM, not to the DM of the
// session that holds the seat: every root view reads dm:seat:<room id>, so the
// seat's own conversation must hold the check-in and its card.
func TestRootSeatCheckinLandsInTheSeatDM(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	const rootID = "ses_20261023-120000-rrrrrrrr"
	recordtest.Write(t, e.mgr.Store.Root, rootID, subagent.Meta{
		ID: rootID, Name: subagent.RoleRoot, Role: subagent.RoleRoot, Service: true,
		RoomID: r.ID, Room: r.Name, Depth: 1, Resumable: true, HarnessSession: "mock-fixture",
		Status: subagent.StatusIdle, Created: time.Now(),
	})
	address := r.Name + ":root"
	msg, err := e.svc.rooms().PostEventInput(room.PostEventInput{
		RoomID: r.ID, Producer: room.ProducerParentCheckin, To: []string{address},
		SessionIDs: map[string]string{address: rootID},
		Event: room.CheckinEvent{
			Parent: rootID, WindowStart: 1, Counts: room.CheckinCounts{Idle: 1},
			Children: []room.CheckinChild{{Name: "kid", State: room.CheckinIdle, Age: "5m", Goal: "g1: work", PR: "no open PRs"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := room.SeatDMConversation(r.ID); msg.Conversation != want {
		t.Fatalf("check-in home = %q, want the seat DM %q", msg.Conversation, want)
	}
	sess, err := e.svc.RootSession(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range sess.Posts {
		if p.ID != msg.ID {
			continue
		}
		if ev, ok := p.Checkin(); !ok || ev.Parent != rootID {
			t.Fatalf("check-in payload = %+v, %v", ev, ok)
		}
		return
	}
	t.Fatalf("the root view does not list its check-in: %+v", sess.Posts)
}
