package roomsvc

import (
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
	"encoding/json"
	"testing"
)

func TestStreamingTurnInvalidatesOnlyItsSession(t *testing.T) {
	e := newEnv(t)
	rec := e.room("stream")
	info := e.start(rec, "worker", nil)
	offsets, members, pins := map[string]int64{}, newMemberWatch(), map[string]string{}
	sig := ""
	e.svc.scan(offsets, &sig, members, pins, true, func(Event) {})
	if _, err := e.mgr.Store.UpdateTurn(info.ID, 1, func(turn *subagent.Turn) { turn.Result = "more streaming output" }); err != nil {
		t.Fatal(err)
	}
	var events []Event
	e.svc.scan(offsets, &sig, members, pins, false, func(ev Event) { events = append(events, ev) })
	sessions := 0
	for _, ev := range events {
		if ev.Kind == "members" {
			t.Fatalf("streaming output invalidated the roster: %+v", ev)
		}
		if ev.Kind == "session" && ev.RoomID == rec.ID {
			sessions++
			if ev.Change == nil || ev.Change.Op != "upsert" || ev.Change.ID != info.ID {
				t.Fatalf("session change = %+v", ev.Change)
			}
		}
	}
	if sessions != 1 {
		t.Fatalf("session events = %d, want 1; events: %+v", sessions, events)
	}
	events = nil
	e.svc.scan(offsets, &sig, members, pins, false, func(ev Event) { events = append(events, ev) })
	if len(events) != 0 {
		t.Fatalf("unchanged scan emitted %+v", events)
	}
}

func TestMemberChangesFlagOnlyAffectedLists(t *testing.T) {
	e := newEnv(t)
	rec := e.room("flags")
	info := e.start(rec, "worker", nil)
	previous := newMemberWatch()
	e.svc.scanMembers([]subagent.Info{info}, []room.Record{rec}, previous, nil, true, func(Event) {})
	check := func(controls, consolidations bool) {
		t.Helper()
		count, sessions := 0, 0
		e.svc.scanMembers([]subagent.Info{info}, []room.Record{rec}, previous, nil, false, func(ev Event) {
			if ev.Kind == "session" {
				sessions++
			}
			if ev.Kind != "members" {
				return
			}
			count++
			if ev.Change == nil || len(ev.Change.Item) == 0 {
				t.Fatalf("member event omitted its canonical item: %+v", ev)
			}
			if ev.Change == nil || ev.Change.ID != info.ID || ev.Change.Controls != controls || ev.Change.Consolidations != consolidations {
				t.Fatalf("member event = %+v", ev)
			}
		})
		if count != 1 {
			t.Fatalf("member events = %d, want 1", count)
		}
		if sessions != 1 {
			t.Fatalf("session events after metadata change = %d, want 1", sessions)
		}
	}
	info.Name = "renamed"
	check(false, false)
	info.Status = subagent.StatusRunning
	check(true, false)
	info.Consolidation = &subagent.ConsolidationState{ID: "operation", Phase: "running"}
	check(false, true)
}

func TestRemovedMemberUsesSharedDeleteOperation(t *testing.T) {
	e := newEnv(t)
	rec := e.room("removed")
	info := e.start(rec, "worker", nil)
	previous := newMemberWatch()
	e.svc.scanMembers([]subagent.Info{info}, []room.Record{rec}, previous, nil, true, func(Event) {})
	var events []Event
	e.svc.scanMembers(nil, []room.Record{rec}, previous, nil, false, func(ev Event) { events = append(events, ev) })
	if len(events) != 2 {
		t.Fatalf("removed member events = %+v", events)
	}
	removed, owner := false, false
	for _, event := range events {
		change := event.Change
		if change == nil || event.Kind != "members" || event.RoomID != rec.ID {
			t.Fatalf("member change = %+v", event)
		}
		if change.ID == info.ID {
			removed = change.Op == "delete" && change.Controls && change.Consolidations
		} else {
			var item Member
			if err := json.Unmarshal(change.Item, &item); err != nil {
				t.Fatal(err)
			}
			owner = item.Owner && item.Children == 0
		}
	}
	if !removed || !owner {
		t.Fatalf("member removal lost its delete or owner count: %+v", events)
	}
}

func TestTurnStateChangeFlagsControlsWithoutMemberStatusChange(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*subagent.Turn)
	}{
		{"held", func(turn *subagent.Turn) { turn.RoomHeld = true }},
		{"steering delivery", func(turn *subagent.Turn) {
			turn.Steering = append(turn.Steering, subagent.Steer{ID: "message", Delivered: true})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.room("turn-state")
			info := e.start(rec, "worker", nil)
			info.Status = subagent.StatusQueued
			previous := newMemberWatch()
			e.svc.scanMembers([]subagent.Info{info}, []room.Record{rec}, previous, nil, true, func(Event) {})
			if _, err := e.mgr.Store.UpdateTurn(info.ID, 1, test.change); err != nil {
				t.Fatal(err)
			}
			count := 0
			e.svc.scanMembers([]subagent.Info{info}, []room.Record{rec}, previous, nil, false, func(ev Event) {
				if ev.Kind != "members" {
					return
				}
				count++
				if ev.Change == nil || !ev.Change.Controls || ev.Change.Consolidations {
					t.Fatalf("turn state change = %+v", ev.Change)
				}
			})
			if count != 1 {
				t.Fatalf("control invalidations = %d, want 1", count)
			}
		})
	}
}

func TestPausedPostChangesHeldCountWithoutTurnOrQueueWrite(t *testing.T) {
	e := newEnv(t)
	rec := e.room("paused-post")
	info := e.start(rec, "worker", nil)
	paused, err := e.svc.PauseRoomSessions(e.ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !paused.State.Paused || paused.State.Held != 0 {
		t.Fatalf("initial controls = %+v", paused.State)
	}
	queue, turn := e.mgr.Store.QueueSignature(info.ID), e.mgr.Store.TurnSignature(info.ID, info.Turns)
	if _, err := e.svc.Post(e.ctx, rec.ID, "@worker pending work", ""); err != nil {
		t.Fatal(err)
	}
	state, err := e.svc.RoomSessionControls(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Held != 1 {
		t.Fatalf("pending post controls = %+v", state)
	}
	if got := e.mgr.Store.QueueSignature(info.ID); got != queue {
		t.Fatalf("paused post wrote a queue: %q != %q", got, queue)
	}
	if got := e.mgr.Store.TurnSignature(info.ID, info.Turns); got != turn {
		t.Fatalf("paused post wrote a turn: %q != %q", got, turn)
	}
}

// The members scan signs a session's current turn from the turn count it has
// already read, without reading the session's record again each tick.
func TestTurnSignatureFollowsTurnWrites(t *testing.T) {
	e := newEnv(t)
	info := e.start(e.room("turn-sig"), "worker", nil)
	if info.Turns < 1 {
		t.Fatalf("started session has %d turns", info.Turns)
	}
	before := e.mgr.Store.TurnSignature(info.ID, info.Turns)
	if before == "" {
		t.Fatal("no signature for the current turn")
	}
	if _, err := e.mgr.Store.UpdateTurn(info.ID, info.Turns, func(turn *subagent.Turn) { turn.HeldPrompt = "held work" }); err != nil {
		t.Fatal(err)
	}
	if after := e.mgr.Store.TurnSignature(info.ID, info.Turns); after == before {
		t.Fatalf("turn write left the signature at %q", after)
	}
	if got := e.mgr.Store.TurnSignature(info.ID, 0); got != "" {
		t.Fatalf("a session with no turn signs as %q", got)
	}
}

func TestNewChildMemberEventUpdatesItsParentCount(t *testing.T) {
	e := newEnv(t)
	rec := e.room("test-room")
	parentID, childID := "ses_20261017-120000-00000001", "ses_20261017-120000-00000002"
	activityRecord(t, e.mgr.Store.Root, parentID, map[string]any{"id": parentID, "name": "parent", "roomId": rec.ID}, nil)
	offsets, previous, pins := map[string]int64{}, newMemberWatch(), map[string]string{}
	roomSig := ""
	e.svc.scan(offsets, &roomSig, previous, pins, true, func(Event) {})
	activityRecord(t, e.mgr.Store.Root, childID, map[string]any{"id": childID, "name": "child", "roomId": rec.ID, "parentSession": parentID}, nil)
	var events []Event
	e.svc.scan(offsets, &roomSig, previous, pins, false, func(event Event) {
		if event.Kind == "members" {
			events = append(events, event)
		}
	})
	if len(events) != 2 {
		t.Fatalf("new child emitted %d member items, want child and parent", len(events))
	}
	expected, err := e.svc.Members(rec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Member{}
	for _, member := range expected {
		byID[member.ID] = member
	}
	for _, event := range events {
		if event.Change == nil {
			t.Fatal("member event has no change")
		}
		var item Member
		if err := json.Unmarshal(event.Change.Item, &item); err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(byID[event.Change.ID])
		got, _ := json.Marshal(item)
		if string(want) != string(got) {
			t.Fatalf("member event differs from API: got %s, want %s", got, want)
		}
	}
	if byID[parentID].Children != 1 {
		t.Fatalf("parent count: %+v", byID[parentID])
	}
}

func TestOwnerSeatChangeCarriesItsMemberAndControlFlag(t *testing.T) {
	e := newEnv(t)
	rec := e.room("owner-item")
	state := newMemberWatch()
	e.svc.scanMembers(nil, []room.Record{rec}, state, nil, true, func(Event) { t.Fatal("initial owner event") })
	if _, _, err := e.svc.rooms().SetSeat(e.ctx, rec.ID, room.Seat{External: !rec.Seat.External}, nil); err != nil {
		t.Fatal(err)
	}
	updated, err := e.svc.rooms().Resolve(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	e.svc.scanMembers(nil, []room.Record{updated}, state, nil, false, func(event Event) { events = append(events, event) })
	if len(events) != 1 || events[0].Change == nil || events[0].Change.ID != rec.Name || !events[0].Change.Controls {
		t.Fatalf("owner seat event: %+v", events)
	}
	var member Member
	if err := json.Unmarshal(events[0].Change.Item, &member); err != nil {
		t.Fatal(err)
	}
	if !member.Owner || member.Seat == nil || member.Seat.External != updated.Seat.External {
		t.Fatalf("owner seat item: %+v", member)
	}
}

func TestRoomPauseWithoutMemberChangesDoesNotReloadRoster(t *testing.T) {
	e := newEnv(t)
	rec := e.room("pause-only")
	offsets, members, pins := map[string]int64{}, newMemberWatch(), map[string]string{}
	signature := ""
	e.svc.scan(offsets, &signature, members, pins, true, func(Event) {})
	if _, err := e.svc.rooms().SetSessionsPaused(e.ctx, rec.ID, true); err != nil {
		t.Fatal(err)
	}
	paused := false
	e.svc.scan(offsets, &signature, members, pins, false, func(event Event) {
		if event.Kind == "members" {
			t.Fatal("pause emitted an empty roster invalidation")
		}
		if event.Kind == "room.paused" {
			paused = true
		}
	})
	if !paused {
		t.Fatal("pause lost its room control event")
	}
}
