package callerid

import (
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
)

func TestCallerEventsKeyOnlyCoordinatorRegistrations(t *testing.T) {
	dir := storetest.DataDir(t)
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	var events []hub.Event
	state.For(s.db).SetPublish(func(ev hub.Event) { events = append(events, ev) })
	for i, actor := range []Identity{{Kind: Worker, Run: "wf_event"}, {Kind: Worker, Run: "wf_event", Agent: 1}, {Kind: Session, Session: "ses_event"}} {
		if err := s.record(t.Context(), time.Now(), "", Process{PID: 100 + i, Start: 1}, actor, "fixture"); err != nil {
			t.Fatal(err)
		}
		want := ""
		if actor.Kind == Worker && actor.Agent == 0 {
			want = actor.Run
		}
		got := events[len(events)-1]
		if got.Topic != "callers" || got.Key != want {
			t.Errorf("actor %+v published topic/key %q/%q, want callers/%q", actor, got.Topic, got.Key, want)
		}
	}
}
