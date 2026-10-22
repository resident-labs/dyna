package engine

import (
	"testing"

	"dyna-agent/internal/hub"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife"
)

// A PR state change goes to the room service, which publishes its rows; the
// raw change never reaches clients. Other changes go to the hub.
func TestPublishStateRoutesPRChanges(t *testing.T) {
	events := hub.New(1)
	publish := publishState(events, roomsvc.New(nil))
	publish(hub.Event{Topic: prstate.Topic})
	if events.Rev() != 0 {
		t.Fatalf("a PR state change reached the hub (rev %d)", events.Rev())
	}
	// A session write wakes the room watch; clients get its members events.
	publish(hub.Event{Topic: sessionlife.Topic, Key: "ses_20261020-000001-aaaaaaaa"})
	if events.Rev() != 0 {
		t.Fatalf("a session write reached the hub (rev %d)", events.Rev())
	}
	publish(hub.Event{Topic: "goals", Room: "room_1"})
	if events.Rev() != 1 {
		t.Fatalf("hub rev %d after a goals change; want 1", events.Rev())
	}
}
