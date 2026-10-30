package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestRecordPressureRecordsEventWithoutDelivery(t *testing.T) {
	s, room, session, ctx := setup(t)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPressure(ctx, record.Box, contract.PressureMax); err != nil {
		t.Fatal(err)
	}
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"box.pressure"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d box.pressure events, want 1", len(events))
	}
	var deliveries int
	if err := s.store.Read().QueryRow("SELECT COUNT(*) FROM deliveries WHERE seq=?", events[0].Seq).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 {
		t.Fatalf("deliveries = %d, want 0", deliveries)
	}
}
