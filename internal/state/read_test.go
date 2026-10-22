package state

import (
	"context"
	"errors"
	"testing"

	"dyna-agent/internal/hub"
)

func countSnapshotThings(ctx context.Context, query Reader) (int, error) {
	var n int
	err := query.QueryRowContext(ctx, "SELECT COUNT(*) FROM things").Scan(&n)
	return n, err
}

func TestReadKeepsOneSnapshot(t *testing.T) {
	w, published := testWriter(t)
	ctx := context.Background()
	err := w.Read(ctx, func(query Reader) error {
		before, err := countSnapshotThings(ctx, query)
		if err != nil {
			return err
		}
		if err := w.Write(ctx, hub.Event{}, insertThing); err != nil {
			return err
		}
		after, err := countSnapshotThings(ctx, query)
		if before != 0 || after != before {
			t.Errorf("snapshot changed from %d to %d", before, after)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := countSnapshotThings(ctx, w.db)
	if err != nil || n != 1 {
		t.Fatalf("committed count = %d, %v; want 1", n, err)
	}
	if len(*published) != 0 || w.Health().Writes != 1 {
		t.Fatalf("read published or counted as a write: events %v, health %+v", *published, w.Health())
	}
}

func TestReadReturnsErrors(t *testing.T) {
	w, _ := testWriter(t)
	failed := errors.New("read failed")
	if err := w.Read(context.Background(), func(Reader) error { return failed }); !errors.Is(err, failed) {
		t.Fatalf("got %v; want callback error", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Read(ctx, func(Reader) error {
		t.Error("cancelled read called its callback")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v; want cancellation", err)
	}
}
