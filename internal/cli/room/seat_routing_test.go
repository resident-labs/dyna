package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// watchPosts receives the JSON records written by the real room watch command.
type watchPosts chan room.Message

func (out watchPosts) Write(data []byte) (int, error) {
	var delivery watchDelivery
	if err := json.Unmarshal(data, &delivery); err != nil {
		return 0, err
	}
	for _, msg := range delivery.Posts {
		out <- msg
	}
	return len(data), nil
}

func TestExternalSeatWatchReceivesRootParentPosts(t *testing.T) {
	storetest.Isolate(t)
	ctx := context.Background()
	mgr := subagent.NewManager()
	svc := notify.New(room.DefaultStore(), mgr)
	rec := workRoom(t)
	if attached, err := svc.AttachSeat(ctx, rec, "mock"); err != nil || !attached.Changed || !attached.Seat.External {
		t.Fatalf("AttachSeat = %+v, %v", attached, err)
	}
	// Retain an idle internal root and its child while the external seat is held.
	root := subagent.Meta{ID: "ses_20261021-000001-aaaaaaaa", Name: room.RootAgent, Role: subagent.RoleRoot,
		Room: rec.Name, RoomID: rec.ID, Status: subagent.StatusIdle, Resumable: true, HarnessSession: "root-history"}
	child := root
	child.ID, child.Name, child.Role, child.ParentSession = "ses_20261021-000002-bbbbbbbb", "child", "", root.ID
	parent := root
	parent.ID, parent.Name, parent.Role = "ses_20261021-000003-cccccccc", "parent-agent", ""
	ordinary := child
	ordinary.ID, ordinary.Name, ordinary.ParentSession = "ses_20261021-000004-dddddddd", "ordinary", parent.ID
	for _, meta := range []subagent.Meta{root, child, parent, ordinary} {
		recordtest.Write(t, mgr.Store.Root, meta.ID, meta)
	}
	post := func(sender subagent.Meta, mentions ...string) notify.Result {
		t.Helper()
		res, err := svc.Post(ctx, notify.PostInput{Sender: notify.Sender{Kind: notify.SenderSession, Room: rec.Name,
			Session: subagent.Info{Meta: sender}}, Mentions: mentions, NeedsRoot: true, Text: "routing evidence"})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	start := post(child, "@root")
	watchCtx, cancel := context.WithCancel(ctx)
	posts, stopped := make(watchPosts, 16), make(chan error, 1)
	cmd := watchCmd()
	cmd.SetArgs([]string{rec.Name, "--since", start.Message.ID})
	cmd.SetOut(posts)
	cmd.SetErr(io.Discard)
	go func() { stopped <- cmd.ExecuteContext(watchCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-stopped:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("room watch stopped: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("room watch did not stop")
		}
	})

	viaParent := post(child, "@parent")
	viaAddress := post(child, "@"+rec.Name+":"+room.RootAgent)
	ordinaryPost := post(ordinary, "@parent")
	end := post(child, "@root")
	seen := map[string]bool{}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for !seen[end.Message.ID] {
		select {
		case msg := <-posts:
			seen[msg.ID] = true
		case <-deadline.C:
			t.Fatal("room watch did not receive the final root post")
		}
	}
	// The last root post is an ordered barrier. No sleep is needed to prove
	// that the preceding parent and explicit root-address posts reached the seat.
	for _, result := range []notify.Result{viaParent, viaAddress} {
		if !seen[result.Message.ID] {
			t.Errorf("external seat watch missed post via %v with targets %v", result.Message.Via, result.Message.To)
		}
		if !reflect.DeepEqual(result.Message.To, []string{rec.Name}) || len(result.Deliveries) != 1 || result.Deliveries[0].Delivery != "room" {
			t.Errorf("external root delivery = %+v, targets %v", result.Deliveries, result.Message.To)
		}
	}
	if seen[ordinaryPost.Message.ID] {
		t.Error("external seat received an ordinary parent post")
	}

	rec, err := svc.Rooms.Resolve(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	seat, changed, deliveries, err := svc.ReturnSeat(ctx, rec, "")
	if err != nil || !changed || seat.External || len(deliveries) != 0 {
		t.Fatalf("ReturnSeat = %+v, %v, %+v, %v", seat, changed, deliveries, err)
	}
	for _, tc := range []struct {
		sender subagent.Meta
		target string
	}{
		{child, rec.Name + ":" + room.RootAgent},
		{ordinary, rec.Name + ":" + parent.Name},
	} {
		res := post(tc.sender, "@parent")
		if !reflect.DeepEqual(res.Message.To, []string{tc.target}) || res.Message.SessionIDs[tc.target] != tc.sender.ParentSession {
			t.Errorf("parent routing after return = %v, session IDs %v", res.Message.To, res.Message.SessionIDs)
		}
		if len(res.Deliveries) == 0 || res.Deliveries[0].Delivery != "session-queued" {
			t.Errorf("parent delivery after return = %+v", res.Deliveries)
		}
	}
}
