package subagent

import (
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
)

func TestDormantMessageDeliveryState(t *testing.T) {
	for _, state := range []string{"waiting", "claimed", "delivered", "cancelled", "claimed-cancelled", "receipt", "unaddressed", "notice", "missing"} {
		t.Run(state, func(t *testing.T) {
			e := roomControlEnv(t)
			info, _ := e.start("SLEEP:1 RESPOND: idle")
			info, _ = e.wait(info.ID, 1)
			to := info.Address()
			if state == "unaddressed" {
				to = info.Room + ":someone-else"
			}
			var msg room.Message
			var err error
			if state == "notice" {
				// A typed instruction note is agent input without a wake.
				msg, err = e.m.rooms().PostEvent(info.RoomID, room.SessionEvent{Type: room.SessionInstructions, Session: to, Detail: "delivery regression"}, to)
			} else {
				msg, err = e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, Kind: "message", To: []string{to}, Text: "delivery regression"})
			}
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case "claimed", "delivered", "notice":
				claimPostsFor(t, e, info.ID, 1, msg.ID)
			}
			if state == "missing" {
				// A claim for a post the room log does not hold: the row is there,
				// the post is gone.
				setDeliveryRow(t, e, info.ID, "msg_0000000000000000", room.DeliveryClaimed, 1)
				cancelPostFor(t, e, info.ID, msg.ID)
			}
			if state == "cancelled" {
				cancelPostFor(t, e, info.ID, msg.ID)
			}
			if state == "claimed-cancelled" {
				// A covered note: the held prompt carried it, and the read cancels
				// the claim so the next turn does not deliver it again.
				claimPostsFor(t, e, info.ID, 1, msg.ID)
				if _, err := e.m.rooms().CoverDeliveries(t.Context(), hub.Event{},
					room.DeliverySet{RoomID: info.RoomID, Keys: room.DeliveryKeys(info.ID, info.Address())},
					[]string{msg.ID}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "delivered" {
				if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.Steering = append(turn.Steering, Steer{ID: msg.ID, Delivered: true}) }); err != nil {
					t.Fatal(err)
				}
			}
			// Claimed work must remain visible even after the room cursor advances.
			_, next, err := e.m.rooms().ReadFrom(info.RoomID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) {
				meta.LastActivity, meta.Finished = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
				meta.Status = StatusDormant
				if state == "claimed" || state == "claimed-cancelled" || state == "unaddressed" {
					meta.RoomCursor = next
				}
				if state == "receipt" {
					meta.InheritedReceipts = map[string]bool{msg.ID: true}
				}
			}); err != nil {
				t.Fatal(err)
			}
			e.doneGoals()
			result, err := closeIdle(e.m, e.ctx, info.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			pending := state == "waiting" || state == "claimed"
			meta, err := e.m.Store.ReadMeta(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			if meta.Closed.IsZero() != pending || len(result.Results) != 1 {
				t.Fatalf("state %s: %+v, closed=%v", state, result, meta.Closed)
			}
			row := result.Results[0]
			if pending {
				for _, detail := range []string{info.ID, info.Address(), msg.ID, msg.From, "dyna room read " + info.Room + " " + msg.ID, "pending messages", "waiting"} {
					if state == "claimed" && detail == "waiting" {
						detail = "turn 1, unacknowledged"
					}
					if !strings.Contains(row.Error, detail) {
						t.Fatalf("missing %q in %q", detail, row.Error)
					}
				}
			} else if !row.OK {
				t.Fatalf("terminal queue record blocked close: %+v", row)
			}
		})
	}
}

func TestDormantInheritedMessageDeliveryState(t *testing.T) {
	for _, state := range []string{"waiting", "delivered", "cancelled", "target-delivered", "notice"} {
		t.Run(state, func(t *testing.T) {
			e := roomControlEnv(t)
			source, _ := e.start("SLEEP:1 RESPOND: source")
			source, _ = e.wait(source.ID, 1)
			target, _ := e.start("SLEEP:1 RESPOND: target")
			target, _ = e.wait(target.ID, 1)
			var msg room.Message
			var err error
			if state == "notice" {
				msg, err = e.m.rooms().PostEvent(source.RoomID, room.SessionEvent{Type: room.SessionInstructions, Session: source.Address(), Detail: "inherited regression"}, source.Address())
			} else {
				msg, err = e.m.rooms().Post(source.RoomID, room.PostInput{Wake: true, From: source.Room, Kind: "message", To: []string{source.Address()}, Text: "inherited regression"})
			}
			if err != nil {
				t.Fatal(err)
			}
			claimPostsFor(t, e, source.ID, 1, msg.ID)
			if state == "cancelled" {
				if _, err := e.m.rooms().CoverDeliveries(t.Context(), hub.Event{},
					room.DeliverySet{RoomID: source.RoomID, Keys: room.DeliveryKeys(source.ID, source.Address())},
					[]string{msg.ID}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "delivered" {
				if _, err := e.m.Store.UpdateTurn(source.ID, 1, func(turn *Turn) { turn.Steering = append(turn.Steering, Steer{ID: msg.ID, Delivered: true}) }); err != nil {
					t.Fatal(err)
				}
			}
			meta, err := e.m.Store.UpdateMeta(target.ID, func(meta *Meta) {
				meta.InheritedInboxes = []InheritedInbox{{Session: source.ID, Address: source.Address(), PendingIDs: []string{msg.ID}}}
				if state == "target-delivered" {
					meta.InheritedReceipts = map[string]bool{msg.ID: true}
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			messages, err := e.m.pendingRoomMessages(meta)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if state == "waiting" {
				want = 1
			}
			if len(messages) != want {
				t.Fatalf("state %s: %+v", state, messages)
			}
		})
	}
}

func TestDormantInformationalPRNoticeDoesNotBlockClose(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		name := "unclaimed"
		if claimed {
			name = "claimed"
		}
		t.Run(name, func(t *testing.T) {
			e := roomControlEnv(t)
			info, _ := e.start("SLEEP:0\nRESPOND: complete")
			info, _ = e.wait(info.ID, 1)
			msg, err := e.m.rooms().PostEventInput(room.PostEventInput{RoomID: info.RoomID, To: []string{info.Address()}, NoWake: true,
				Event: room.PREvent{Type: room.PRUpdate, Label: "PR #1", Location: "github.com/o/r#1", ChangeID: "github.com/owner/repo#1", Detail: "Watching stopped."}})
			if err != nil {
				t.Fatal(err)
			}
			if claimed {
				claimPostsFor(t, e, info.ID, 1, msg.ID)
			}
			if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) {
				meta.LastActivity, meta.Finished = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
				meta.Status = StatusDormant
			}); err != nil {
				t.Fatal(err)
			}
			e.doneGoals()
			result, err := closeIdle(e.m, e.ctx, info.RoomID)
			if err != nil || result.Succeeded != 1 || result.Failed != 0 {
				t.Fatalf("informational notice blocked close: %+v, %v", result, err)
			}
			meta, err := e.m.Store.ReadMeta(info.ID)
			if err != nil || meta.Closed.IsZero() || meta.Turns != 1 {
				t.Fatalf("dormant session did not close without a new turn: %+v, %v", meta, err)
			}
			if _, err := e.m.rooms().Message(info.RoomID, msg.ID); err != nil {
				t.Fatalf("close removed the notice: %v", err)
			}
		})
	}
}
