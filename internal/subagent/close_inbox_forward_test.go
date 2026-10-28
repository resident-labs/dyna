package subagent

import (
	"testing"

	"dyna-agent/internal/room"
)

func TestCloseForwardsPendingPostsOnce(t *testing.T) {
	for _, withParent := range []bool{true, false} {
		t.Run(map[bool]string{true: "parent", false: "root seat"}[withParent], func(t *testing.T) {
			e := newTestEnv(t)
			source := completedFixtureSession(t, e)
			var parent Info
			if withParent {
				parent = completedFixtureSession(t, e)
				if _, err := e.m.Store.UpdateMeta(source.ID, func(m *Meta) { m.ParentSession = parent.ID; m.Depth = 2 }); err != nil {
					t.Fatal(err)
				}
			}
			original, err := e.m.rooms().Post(source.RoomID, room.PostInput{From: source.Room, To: []string{source.Address()}, Wake: true, Text: "pending work"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.m.Close(e.ctx, source.ID); err != nil {
				t.Fatal(err)
			}
			closed, err := e.m.Store.Info(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			queue, err := e.m.Store.SessionQueue(e.m.rooms(), source.ID)
			if err != nil || len(queue) != 1 || queue[0].State != QueueForwarded {
				t.Fatalf("source queue = %+v, %v", queue, err)
			}
			id := queue[0].ForwardedTo
			if queue[0].ForwardedTo != id {
				t.Fatalf("forward receipt = %+v", queue[0])
			}
			if id == "" {
				t.Fatal("pending post was not marked forwarded")
			}
			forwarded, err := e.m.rooms().Message(source.RoomID, id)
			want := source.Room
			if withParent {
				want = parent.Address()
			}
			if err != nil || len(forwarded.To) != 1 || forwarded.To[0] != want || forwarded.Forward == nil || forwarded.Forward.ID != original.ID {
				t.Fatalf("forward = %+v, %v; want %s", forwarded, err, want)
			}
			if !withParent {
				seat, ok, err := e.m.rooms().SeatDelivery(source.RoomID, id)
				if err != nil || !ok || seat.Mode != room.DeliveryWake {
					t.Fatalf("root seat = %+v, %v, %v", seat, ok, err)
				}
			}
			if err := e.m.forwardClosedInbox(e.ctx, closed); err != nil {
				t.Fatal(err)
			}
			all, err := e.m.rooms().Messages(source.RoomID)
			if err != nil || len(all) != 2 {
				t.Fatalf("posts after retry = %+v, %v", all, err)
			}
			late, err := e.m.rooms().Post(source.RoomID, room.PostInput{From: source.Room, To: []string{source.Address()}, Wake: true, Text: "late work"})
			if err != nil {
				t.Fatal(err)
			}
			if err := e.m.TickSessionTargets(e.ctx, []string{source.ID}); err != nil {
				t.Fatal(err)
			}
			receipts, err := e.m.Store.forwardedPosts(source.ID)
			if err != nil || receipts[late.ID] == "" {
				t.Fatalf("late post not forwarded: %+v %v", receipts, err)
			}
		})
	}
}
