package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestCombinedEventsIndependentBoundsAndPagination(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, room := range []contract.RoomID{"selected", "other"} {
		if err := s.CreateRoom(ctx, room, string(room), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var all []contract.Event
	for i := 0; i < 620; i++ {
		room := contract.RoomID("other")
		if i%2 == 0 {
			room = "selected"
		}
		var body contract.EventBody = contract.PostCreated{Text: "message"}
		if i%3 == 0 {
			body = contract.RoomPaused{}
		}
		e := contract.Event{ID: contract.EventID(fmt.Sprintf("ev_%d", i)), Room: room, Time: time.Now(), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body}
		if err := s.RecordEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	all, err = s.OwnerEvents(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	attention := map[int64]bool{}
	for _, e := range all {
		attention[e.Seq] = true
	}
	selected, err := s.Events(ctx, "selected", 0, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	selectedSeq := map[int64]bool{}
	for _, e := range selected {
		selectedSeq[e.Seq] = true
	}
	for _, bounds := range [][2]int64{{1, 400}, {400, 1}, {0, 0}} {
		a, b := bounds[0], bounds[1]
		var got []int64
		for {
			page, err := s.CombinedEvents(ctx, "selected", a, b, 256)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range page {
				got = append(got, e.Seq)
			}
			if len(page) < 256 {
				break
			}
			last := page[len(page)-1].Seq
			a = max(a, last)
			b = max(b, last)
		}
		var want []int64
		for seq := int64(1); seq <= 620; seq++ {
			if seq > bounds[0] && attention[seq] || seq > bounds[1] && selectedSeq[seq] {
				want = append(want, seq)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("bounds %v: got %v want %v", bounds, got, want)
		}
	}
}
