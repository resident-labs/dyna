package engine

import (
	"context"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/websvc"
)

type callPushRecorder struct{ events []websvc.Event }

func (r *callPushRecorder) Notify(_ context.Context, event websvc.Event) {
	r.events = append(r.events, event)
}

func TestCallRequestPushFallbackAndCloseDelivery(t *testing.T) {
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "calls"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.rooms.Notify.Rooms.SetSeat(t.Context(), rec.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	st := s.rooms.Notify.CallRequestStore()
	row, _, err := st.Ask(rec.ID, "CI needs your sign-in", "root")
	if err != nil {
		t.Fatal(err)
	}
	push := &callPushRecorder{}
	deliver := func(at time.Time) {
		t.Helper()
		if _, err := deliverCallRequests(t.Context(), s.rooms, push, at); err != nil {
			t.Fatal(err)
		}
	}
	deliver(row.AskedAt.Add(5*time.Second - time.Nanosecond))
	if len(push.events) != 0 {
		t.Fatal("push sent before five seconds")
	}
	deliver(row.AskedAt.Add(5 * time.Second))
	deliver(row.AskedAt.Add(24 * time.Hour))
	if len(push.events) != 1 || push.events[0].Call != row.ID || push.events[0].Text != row.Reason || push.events[0].Kind != "call" {
		t.Fatalf("pushes: %+v", push.events)
	}
	if _, _, err := st.Close(rec.ID, row.ID, "declined", "owner"); err != nil {
		t.Fatal(err)
	}
	deliver(row.AskedAt.Add(24 * time.Hour))
	deliver(row.AskedAt.Add(24 * time.Hour))
	posts, err := s.rooms.Notify.Rooms.Messages(rec.ID)
	if err != nil || len(posts) != 1 || !posts[0].Addressed(rec.Name) {
		t.Fatalf("close posts: %+v, %v", posts, err)
	}
	ev, ok := posts[0].Call()
	if !ok || ev.Type != room.CallDeclined || ev.Call != row.ID {
		t.Fatalf("close event = %+v, %v", ev, ok)
	}
	if posts[0].Kind != string(room.FamilyCall) || posts[0].From != rec.Name || posts[0].Human {
		t.Fatalf("close post is not a typed call event: %+v", posts[0])
	}
	if posts[0].Text != "The owner declined the call c1." {
		t.Fatalf("close text = %q", posts[0].Text)
	}
	saved, err := st.Get(rec.ID, row.ID)
	if err != nil || !saved.CloseDelivered {
		t.Fatalf("close receipt: %+v, %v", saved, err)
	}
	// Simulate the retry after a crash between the post and its receipt.
	if _, _, err := st.Ask(rec.ID, "check deployment", "root"); err != nil {
		t.Fatal(err)
	}
	second, err := st.Open(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Shown(rec.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	deliver(second.AskedAt.Add(5 * time.Second))
	if len(push.events) != 1 {
		t.Fatal("shown request sent a push")
	}
	if _, _, err := st.Close(rec.ID, second.ID, "withdrawn", "root"); err != nil {
		t.Fatal(err)
	}
	id := questions.PostID(rec.ID, second.ID, "call-closed")
	if _, err := s.rooms.Notify.Rooms.PostEventInput(room.PostEventInput{RoomID: rec.ID, ID: id, Event: room.CallEvent{Type: room.CallWithdrawn, Call: second.ID}, To: []string{rec.Name}}); err != nil {
		t.Fatal(err)
	}
	deliver(second.AskedAt.Add(5 * time.Second))
	posts, err = s.rooms.Notify.Rooms.Messages(rec.ID)
	if err != nil || len(posts) != 2 {
		t.Fatalf("retry duplicated close: %+v, %v", posts, err)
	}
	if withdrawn, ok := posts[1].Call(); !ok || withdrawn.Type != room.CallWithdrawn {
		t.Fatalf("retried close = %+v, %v", withdrawn, ok)
	}
}

// The call ring has no clock tick: a ringing call's push goes out when it is
// due, and a call-requests event delivers its close notice.
func TestCallRingFiresAtTheDueTimeAndOnEvents(t *testing.T) {
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "ring"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.rooms.Notify.Rooms.SetSeat(t.Context(), rec.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	st := s.rooms.Notify.CallRequestStore()
	row, _, err := st.Ask(rec.ID, "Approve the deploy", "root")
	if err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	clock.now = row.AskedAt
	ctx, cancel := context.WithCancel(t.Context())
	set := clock.timers(ctx)
	push := &syncPushRecorder{}
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { defer close(done); runCallRequests(ctx, s.rooms, push, wake, set) }()
	defer func() { cancel(); <-done }()
	awaitTimerAt(t, set, clock, timerCallRing, row.AskedAt.Add(ringPush))
	clock.advance(ringPush - time.Millisecond)
	if n := len(push.sent()); n != 0 {
		t.Fatal("push sent before the call rang five seconds")
	}
	clock.advance(time.Millisecond)
	awaitCall(t, func() bool { return len(push.sent()) == 1 })
	if _, _, err := st.Close(rec.ID, row.ID, "joined", "owner"); err != nil {
		t.Fatal(err)
	}
	wakeWorker(wake)
	awaitCall(t, func() bool { saved, err := st.Get(rec.ID, row.ID); return err == nil && saved.CloseDelivered })
	if sent := push.sent(); len(sent) != 1 || sent[0].Call != row.ID {
		t.Fatalf("pushes: %+v", sent)
	}
}

// syncPushRecorder records pushes sent from the call ring's goroutine.
type syncPushRecorder struct {
	mu     sync.Mutex
	events []websvc.Event
}

func (r *syncPushRecorder) Notify(_ context.Context, event websvc.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *syncPushRecorder) sent() []websvc.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// awaitCall waits for the call ring's goroutine to reach a stored state.
func awaitCall(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("the call ring did not deliver")
		}
		runtime.Gosched()
	}
}
