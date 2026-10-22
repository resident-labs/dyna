package hub

import "testing"

func TestPublishNumbersEventsInTheEpoch(t *testing.T) {
	h := New(1700)
	events, _, _ := h.Subscribe()
	h.Publish(Event{Topic: "pins", Room: "room_1"})
	h.Publish(Event{Topic: "rooms", Epoch: 9, Rev: 9})
	for _, want := range []Event{{Topic: "pins", Room: "room_1", Epoch: 1700, Rev: 1}, {Topic: "rooms", Epoch: 1700, Rev: 2}} {
		if got := <-events; got != want {
			t.Fatalf("event %+v; want %+v", got, want)
		}
	}
}

func TestStatsReportLagAndDroppedStreams(t *testing.T) {
	h := New(1)
	h.Subscribe()
	h.Publish(Event{Topic: "posts"})
	h.Publish(Event{Topic: "posts"})
	if st := h.Stats(); st != (Stats{Seq: 2, Streams: 1, MaxLag: 2}) {
		t.Fatalf("stats with one slow stream: %+v", st)
	}
	for range Buffer {
		h.Publish(Event{Topic: "posts"})
	}
	if st := h.Stats(); st.Streams != 0 || st.Dropped != 1 || st.MaxLag != 0 {
		t.Fatalf("stats after the stream overflowed: %+v; want it dropped and counted", st)
	}
}

func TestHubDropsASlowStream(t *testing.T) {
	h := New(1)
	events, _, unsubscribe := h.Subscribe()
	for range Buffer + 1 {
		h.Publish(Event{Topic: "posts"})
	}
	if h.Stats().Streams != 0 {
		t.Fatal("a full stream must be dropped")
	}
	n := 0
	for range events {
		n++
	}
	if n != Buffer {
		t.Fatalf("buffered %d", n)
	}
	unsubscribe() // safe after a drop
}

func TestSinceResetsACallerTooFarBehind(t *testing.T) {
	h := New(1)
	h.Publish(Event{Topic: "rooms"})
	h.Publish(Event{Topic: "pins"})
	if events, last, reset, _ := h.Since(1, 1); reset || last != 2 || len(events) != 1 || events[0].Topic != "pins" {
		t.Fatalf("Since(1) = %+v, %d, %v; want the pins event", events, last, reset)
	}
	for range Keep {
		h.Publish(Event{Topic: "posts"})
	}
	if _, _, reset, _ := h.Since(1, 1); !reset {
		t.Fatal("a caller behind the ring must reset")
	}
	if _, _, reset, _ := h.Since(1, h.Rev()+1); !reset {
		t.Fatal("a caller ahead of the hub must reset")
	}
}

// A reconnect in the same epoch gets the events it missed, not a reset.
func TestResumeReplaysMissedEvents(t *testing.T) {
	h := New(7)
	for _, topic := range []string{"rooms", "pins", "posts"} {
		h.Publish(Event{Topic: topic})
	}
	epoch, rev, ok := ParseID(Event{Epoch: 7, Rev: 1}.ID())
	if !ok || epoch != 7 || rev != 1 {
		t.Fatalf("ParseID(ID()) = %d, %d, %v; want 7, 1, true", epoch, rev, ok)
	}
	events, last, reset, _ := h.Since(epoch, rev)
	if reset || last != 3 || len(events) != 2 || events[0].Topic != "pins" || events[1].Topic != "posts" {
		t.Fatalf("resume after 7:1 = %+v, %d, %v; want pins and posts", events, last, reset)
	}
}

// A stream dropped as too slow resumes from the last event it got.
func TestDroppedStreamResumes(t *testing.T) {
	h := New(7)
	events, _, _ := h.Subscribe()
	for range Buffer + 3 {
		h.Publish(Event{Topic: "posts"})
	}
	var got Event
	for ev := range events {
		got = ev
	}
	missed, last, reset, _ := h.Since(got.Epoch, got.Rev)
	if reset || len(missed) != 3 || last != Buffer+3 || missed[0].Rev != got.Rev+1 {
		t.Fatalf("resume after the drop at %s = %d events, last %d, reset %v; want the 3 dropped events", got.ID(), len(missed), last, reset)
	}
}

// After an engine restart the old epoch's events are gone: the client
// reloads once instead of replaying.
func TestNewEpochResets(t *testing.T) {
	old := New(7)
	old.Publish(Event{Topic: "posts"})
	h := New(8)
	h.Publish(Event{Topic: "posts"})
	h.Publish(Event{Topic: "posts"})
	if _, _, reset, _ := h.Since(7, 1); !reset {
		t.Fatal("a client of the old epoch must reset")
	}
	for _, id := range []string{"", "7", "x:1", "7:y"} {
		if _, _, ok := ParseID(id); ok {
			t.Errorf("ParseID(%q) succeeded", id)
		}
	}
}
