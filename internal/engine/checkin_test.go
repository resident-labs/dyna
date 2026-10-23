package engine

import (
	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/settings"
	"testing"
)

func TestCheckInSettingAndTurnEventsWakeScheduler(t *testing.T) {
	s := testServices(t, false)
	signals := newWorkerSignals(nil)
	s.settingsChanged = func() { wakeWorker(signals.channels["checkins"]) }
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	take := func(want bool) {
		t.Helper()
		select {
		case <-signals.channels["checkins"]:
			if !want {
				t.Fatal("unexpected check-in wake")
			}
		default:
			if want {
				t.Fatal("missing check-in wake")
			}
		}
	}
	for _, value := range []string{"0", "1m"} {
		if _, err := calls["SetSetting"](ctx, cliArgs(t, settings.KeySessionCheckInInterval, value)); err != nil {
			t.Fatal(err)
		}
		take(true)
	}
	if _, err := calls["SetSetting"](ctx, cliArgs(t, settings.KeySessionCheckInInterval, "-1m")); err == nil {
		t.Fatal("negative interval accepted")
	}
	take(false)
	signals.wake(roomsvc.Event{Kind: "members"})
	take(true)
	signals.wake(roomsvc.Event{Kind: "posts", Posts: []roomsvc.Post{{Message: room.Message{Kind: "completed", Turn: 1}}}})
	take(true)
}
