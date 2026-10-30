package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestRecordImageEvent(t *testing.T) {
	s, room, _, ctx := setup(t)
	if err := s.Record(ctx, room, contract.ImageBuilt{Host: "h", Tag: "tag"}); err != nil {
		t.Fatal(err)
	}
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"image.built"})
	if err != nil || len(events) != 1 || events[0].From.Kind != contract.PrincipalEngine {
		t.Fatalf("events %+v, error %v", events, err)
	}
}
