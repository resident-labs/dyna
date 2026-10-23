package room

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
)

// watchFixture is a room with the watch's own seat lookup, so the tests post
// real posts and the stored delivery rows decide what the monitor holds and
// delivers.
func watchFixture(t *testing.T) (watchSeat, func(room.PostInput) room.Message, func(room.Event, ...string) room.Message) {
	t.Helper()
	storetest.Isolate(t)
	st := room.DefaultStore()
	rec := workRoom(t)
	seat := func(msg room.Message) (room.DeliveryMode, bool, error) {
		d, ok, err := st.SeatDelivery(rec.ID, msg.ID)
		return d.Mode, ok, err
	}
	event := func(ev room.Event, to ...string) room.Message {
		t.Helper()
		msg, err := st.PostEvent(rec.ID, ev, to...)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	return seat, func(in room.PostInput) room.Message {
		t.Helper()
		msg, err := st.Post(rec.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}, event
}

func TestWatchHoldsLoneQuietPostUntilWake(t *testing.T) {
	seat, post, _ := watchFixture(t)
	var out bytes.Buffer
	write := watchWriter(&out, seat, false, false)
	quiet := post(room.PostInput{From: "work:lead", Kind: "message", To: []string{"work"}, Text: "Cleanup complete"})
	if err := write(quiet); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("lone quiet post printed before a wake: %s", out.Bytes())
	}
	wake := post(room.PostInput{Wake: true, From: "work:lead", Kind: "service-alert", To: []string{"work"}, Text: "Main is red"})
	if err := write(wake); err != nil {
		t.Fatal(err)
	}
	var got watchDelivery
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != wake.ID || !reflect.DeepEqual(got.Posts, []room.Message{quiet, wake}) || bytes.Count(out.Bytes(), []byte{'\n'}) != 1 {
		t.Fatalf("quiet-plus-wake delivery = %s", out.Bytes())
	}
	out.Reset()
	if err := write(wake); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got.Posts, []room.Message{wake}) {
		t.Fatalf("held context repeated: %s, %v", out.Bytes(), err)
	}
}

func TestWatchAllKeepsRawQuietPosts(t *testing.T) {
	seat, post, event := watchFixture(t)
	for _, in := range []room.PostInput{
		{From: "work:lead", Kind: "message", To: []string{"work"}, Text: "Cleanup complete"},
	} {
		msg := post(in)
		var out bytes.Buffer
		if err := watchWriter(&out, seat, true, false)(msg); err != nil {
			t.Fatal(err)
		}
		var got room.Message
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, msg) {
			t.Fatalf("raw --all post = %s, %v", out.Bytes(), err)
		}
	}
	// A typed event is a raw post too, never reworded by the watch.
	turn := event(room.TurnEvent{Session: "work:root", SessionID: "ses_root", Turn: 1, State: room.TurnCompleted, Summary: "Root history"}, "work")
	var out bytes.Buffer
	if err := watchWriter(&out, seat, true, false)(turn); err != nil {
		t.Fatal(err)
	}
	var got room.Message
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, turn) {
		t.Fatalf("raw --all event = %s, %v", out.Bytes(), err)
	}
}

func TestWatchWakeAndOwnerEchoRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   room.PostInput
		want bool
	}{
		{"owner request", room.PostInput{Wake: true, From: "work", Human: true, Kind: "message", To: []string{"work"}, Text: "Fix it"}, true},
		// The owner's DM to an agent is quiet context for both roots: the
		// agent answers it, and root reads what was said with the next wake.
		{"owner DM to an agent", room.PostInput{Wake: true, From: "work", Human: true, Kind: "message", Via: []string{"@worker"}, To: []string{"work:worker"}, Text: "Fix it"}, false},
		{"owner failure", room.PostInput{Wake: true, From: "work:worker", Kind: "message", NeedsRoot: true, To: []string{"work"}, Text: "It failed"}, true},
		// The owner's own shell and API posts name the room as the sender. Only
		// the NeedsRoot mark says that one asks the seat to act.
		{"own owner post that needs root", room.PostInput{Wake: true, From: "work", Kind: "message", NeedsRoot: true, To: []string{"work"}, Text: "Choose the merge order"}, true},
		{"own owner post without a mark", room.PostInput{Wake: true, From: "work", Kind: "message", To: []string{"work"}, Text: "Paused the queue"}, false},
		{"own owner alert", room.PostInput{Wake: true, From: "work", Kind: "service-alert", To: []string{"work"}, Text: "Main is red"}, false},
		{"another recipient", room.PostInput{Wake: true, From: "work:worker", Kind: "service-alert", To: []string{"work:other"}, Text: "Main is red"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seat, post, _ := watchFixture(t)
			var out bytes.Buffer
			write := watchWriter(&out, seat, false, false)
			quiet := post(room.PostInput{From: "work:lead", Kind: "message", To: []string{"work"}, Text: "Cleanup complete"})
			if err := write(quiet); err != nil {
				t.Fatal(err)
			}
			msg := post(tc.in)
			if err := write(msg); err != nil {
				t.Fatal(err)
			}
			if !tc.want {
				if out.Len() != 0 {
					t.Fatalf("unrelated or own post flushed context: %s", out.Bytes())
				}
				return
			}
			var got watchDelivery
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got.Posts, []room.Message{quiet, msg}) {
				t.Fatalf("root wake delivery = %s, %v", out.Bytes(), err)
			}
		})
	}
}

func TestWatchHoldsQuietContextThroughRootSelfPosts(t *testing.T) {
	seat, post, event := watchFixture(t)
	var out bytes.Buffer
	write := watchWriter(&out, seat, false, false)
	quiet := post(room.PostInput{From: "work:lead", Kind: "message", To: []string{"work"}, Text: "Keep the queue paused"})
	if err := write(quiet); err != nil {
		t.Fatal(err)
	}
	selfs := []room.Message{
		post(room.PostInput{From: "work:root", Kind: "message", To: []string{"work"}, Text: "Root history"}),
		event(room.TurnEvent{Session: "work:root", SessionID: "ses_root", Turn: 1, State: room.TurnCompleted, Summary: "Root history"}, "work"),
		event(room.TurnEvent{Session: "work:root", SessionID: "ses_root", Turn: 2, State: room.TurnFailed, Summary: "Root history", Error: "boom"}, "work"),
	}
	for _, self := range selfs {
		if err := write(self); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 {
			t.Errorf("root self-post flushed held context: %s", out.Bytes())
		}
	}
	alert := post(room.PostInput{Wake: true, From: "work:lead", Kind: "service-alert", To: []string{"work"}, Text: "Main is red"})
	if err := write(alert); err != nil {
		t.Fatal(err)
	}
	var got watchDelivery
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ID != alert.ID || !reflect.DeepEqual(got.Posts, []room.Message{quiet, alert}) {
		t.Fatalf("held context did not wait for the real alert: %s, %v", out.Bytes(), err)
	}
}

// The engine's timed-flush notice, posted after 15 quiet minutes, delivers
// the held context as one block. Its shape must stay in sync with the notice
// rootFlush.fire posts (internal/engine/rootflush.go).
func TestWatchTimedFlushNoticeDeliversHeldContext(t *testing.T) {
	seat, post, event := watchFixture(t)
	var out bytes.Buffer
	write := watchWriter(&out, seat, false, false)
	quiet := post(room.PostInput{From: "work:lead", Kind: "message", To: []string{"work"}, Text: "Keep the queue paused"})
	if err := write(quiet); err != nil {
		t.Fatal(err)
	}
	flush := event(room.EngineEvent{Minutes: 15}, "work")
	if err := write(flush); err != nil {
		t.Fatal(err)
	}
	var got watchDelivery
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ID != flush.ID || !reflect.DeepEqual(got.Posts, []room.Message{quiet, flush}) {
		t.Fatalf("timed-flush delivery = %s, %v", out.Bytes(), err)
	}
}

func TestWatchHoldsPassiveInputUntilWake(t *testing.T) {
	seat, post, event := watchFixture(t)
	var out bytes.Buffer
	write := watchWriter(&out, seat, false, false)
	quiet := post(room.PostInput{From: "work:worker", Kind: "message", To: []string{"work"}, Text: "Repair the test"})
	passive := event(room.SessionEvent{Type: room.SessionInstructions, Session: "work:lead", Detail: "Updated room rules"}, "work:root")
	for _, msg := range []room.Message{quiet, passive} {
		if err := write(msg); err != nil {
			t.Fatal(err)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("passive input released held context: %s", out.Bytes())
	}
	wake := post(room.PostInput{Wake: true, From: "work:worker", Kind: "service-alert", To: []string{"work"}, Text: "Main is red"})
	if err := write(wake); err != nil {
		t.Fatal(err)
	}
	var got watchDelivery
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ID != wake.ID || !reflect.DeepEqual(got.Posts, []room.Message{quiet, passive, wake}) {
		t.Fatalf("passive input must arrive once with the next wake: %s, %v", out.Bytes(), err)
	}
}
