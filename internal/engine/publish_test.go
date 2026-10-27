package engine

import (
	"testing"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store/storetest"
)

// A PR state change goes to the room service, which publishes its rows; the
// raw change never reaches clients. Other changes go to the hub.
func TestPublishStateRoutesPRChanges(t *testing.T) {
	storetest.Isolate(t)
	events := hub.New(1)
	signals := newWorkerSignals(nil)
	publish := publishState(events, roomsvc.New(nil), signals)
	publish(hub.Event{Topic: prstate.Topic})
	if events.Rev() != 0 {
		t.Fatalf("a PR state change reached the hub (rev %d)", events.Rev())
	}
	// A session write wakes the room watch; clients get its members events.
	publish(hub.Event{Topic: sessionlife.Topic, Key: "ses_20261020-000001-aaaaaaaa"})
	if events.Rev() != 0 {
		t.Fatalf("a session write reached the hub (rev %d)", events.Rev())
	}
	for _, topic := range []string{"goals", "questions", "notices", "sync"} {
		publish(hub.Event{Topic: topic, Room: "room_1"})
		if events.Rev() != 0 {
			t.Fatalf("%s commit bypassed the room watch publisher", topic)
		}
	}
	signals.channels["call-requests"] = make(chan struct{}, 1)
	publish(hub.Event{Topic: "call-requests", Room: "room_1"})
	if events.Rev() != 0 {
		t.Fatal("call-request commit bypassed the room watch publisher")
	}
	select {
	case <-signals.channels["call-requests"]:
	default:
		t.Fatal("call-request commit did not wake the notifier")
	}
	publish(hub.Event{Topic: localci.Topic, Key: localci.ResultEventKey("head").String()})
	for _, worker := range []string{"prs", "merges"} {
		select {
		case <-signals.channels[worker]:
		default:
			t.Fatalf("local CI completion did not wake %s", worker)
		}
	}
	// A cache write is a localci key, but it is not a wake family.
	publish(hub.Event{Topic: localci.Topic, Key: localci.CachesEventKey("node").String()})
	for _, worker := range []string{"prs", "merges"} {
		select {
		case <-signals.channels[worker]:
			t.Fatalf("a cache cleanup woke %s", worker)
		default:
		}
	}
}

func TestPublishLocalCIRefreshesOnlyRoomsUsingHead(t *testing.T) {
	storetest.Isolate(t)
	rooms := roomsvc.New(nil)
	st := prstate.State{Version: 1, Records: map[string]*prstate.Record{
		"a": {Key: "a", RoomID: "room_a", Head: "pr-head"},
		"b": {Key: "b", RoomID: "room_b", Head: "other-head"},
	}, MergeTrials: map[string]*prstate.MergeTrial{"repo": {Head: "trial-head", Changes: []prstate.MergeTrialChange{{Key: "a"}}, Stacks: []prstate.MergeTrialStack{{Head: "stack-head"}}}}}
	if err := prstate.Write(rooms.PRs.Watcher().Store.Root, st); err != nil {
		t.Fatal(err)
	}
	for _, head := range []string{"pr-head", "trial-head", "stack-head", "unrelated"} {
		events := hub.New(1)
		stream, _, closeStream := events.Subscribe()
		defer closeStream()
		publishState(events, rooms, newWorkerSignals(nil))(hub.Event{Topic: localci.Topic, Key: localci.ResultEventKey(head).String()})
		want := uint64(1)
		if head == "unrelated" {
			want = 0
		}
		if events.Rev() != want {
			t.Fatalf("head %s: got %d events; want %d", head, events.Rev(), want)
		}
		if want == 1 {
			event := <-stream
			if event.Topic != "prs" || event.Room != "room_a" {
				t.Fatalf("head %s: unexpected event %+v", head, event)
			}
		}
	}
}

// A machine edit wakes the CI workers, the probe and the cache cleanup, since a
// new or enabled CI machine needs its own daily cleanup; a probe result wakes
// none of them, or every five-minute probe would wake all three.
func TestPublishMachineChangesWakeOnlyTheRegistryWorkers(t *testing.T) {
	storetest.Isolate(t)
	events := hub.New(1)
	signals := newWorkerSignals(nil)
	publish := publishState(events, roomsvc.New(nil), signals)
	drain := func(worker string) bool {
		select {
		case <-signals.channels[worker]:
			return true
		default:
			return false
		}
	}

	publish(hub.Event{Topic: machines.Topic, Key: "machine:hermeslab"})
	for _, worker := range []string{"prs", "merges", "probe", "caches"} {
		if !drain(worker) {
			t.Fatalf("a machine change did not wake %s", worker)
		}
	}

	rev := events.Rev()
	publish(hub.Event{Topic: machines.Topic, Key: "health:hermeslab"})
	for _, worker := range []string{"prs", "merges", "probe", "caches"} {
		if drain(worker) {
			t.Fatalf("a probe result woke %s", worker)
		}
	}
	// Clients still get the result, so the machine page can show the health.
	if events.Rev() != rev+1 {
		t.Fatalf("a probe result did not reach the hub (rev %d)", events.Rev())
	}
}
