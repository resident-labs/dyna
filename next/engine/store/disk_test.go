package store

import (
	"context"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestDiskLowCrossing(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	if err := s.AddHost(ctx, box.Host{Name: "h", State: box.HostReady, DiskBytes: 20 * box.GiB, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, free := range []int64{19 * box.GiB, 18 * box.GiB, 20 * box.GiB, 19 * box.GiB} {
		if err := s.RecordHostHeartbeat(ctx, "h", true, &free, now); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.Events(ctx, "", 0, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events", len(events))
	}
	for _, event := range events {
		low, ok := event.Body.(contract.HostDiskLow)
		if !ok || low.Host != "h" || low.FreeBytes != 19*box.GiB || low.FloorBytes != 20*box.GiB {
			t.Fatalf("event: %#v", event.Body)
		}
	}
}
