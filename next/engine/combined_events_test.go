package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/live"
	"github.com/resident-labs/dyna/engine/store"
)

func TestCombinedEventsReplayLiveAndOverflow(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Service{store: st, live: live.New(nil)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.CreateRoom(ctx, "r", "r", time.Now()); err != nil {
		t.Fatal(err)
	}
	record := func(i int) {
		t.Helper()
		if err := st.RecordEvent(ctx, contract.Event{ID: contract.EventID(fmt.Sprintf("e%d", i)), Room: "r", Time: time.Now(), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: "x"}}); err != nil {
			t.Fatal(err)
		}
	}
	record(1)
	if _, e := s.CombinedEvents(ctx, contract.Principal{Kind: contract.PrincipalSession}, "r", 0, 0); e == nil {
		t.Fatal("session allowed")
	}
	ch, e := s.CombinedEvents(ctx, contract.Principal{Kind: contract.PrincipalOwner}, "r", 0, 0)
	if e != nil {
		t.Fatal(e)
	}
	// Leave output blocked while the bounded persisted subscription overflows.
	for i := 2; i <= 310; i++ {
		record(i)
	}
	for want := int64(1); want <= 310; want++ {
		select {
		case got := <-ch:
			if got.Seq != want {
				t.Fatalf("got %d want %d", got.Seq, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	record(311)
	select {
	case got := <-ch:
		if got.Seq != 311 {
			t.Fatalf("live seq %d", got.Seq)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// A global live subscription also carries owner-only transient notifications.
	s.live.Publish("r", contract.Event{Room: "r", Body: contract.TranscriptChanged{Session: "s", Turn: "t"}}, true)
	select {
	case got := <-ch:
		if got.Body.EventType() != "transcript.changed" {
			t.Fatalf("transient %v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
