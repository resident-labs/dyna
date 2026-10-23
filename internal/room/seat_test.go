package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A new room's seat is the internal root, and the room record stores nothing
// for it. Attach and detach each change the holder once; repeating one keeps
// the first Since.
func TestSetSeatMovesTheHolderOnce(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seat, changed, err := s.SetSeat(ctx, "alpha", Seat{External: true}, nil)
	if err != nil || !changed || !seat.External || seat.Since.IsZero() {
		t.Fatalf("attach = %+v, %v, %v", seat, changed, err)
	}
	again, changed, err := s.SetSeat(ctx, rec.ID, Seat{External: true}, nil)
	if err != nil || changed || !again.External || !again.Since.Equal(seat.Since) {
		t.Fatalf("second attach = %+v, %v, %v; want unchanged %+v", again, changed, err, seat)
	}
	if got, _ := s.Resolve("alpha"); !got.Seat.External || !got.Seat.Since.Equal(seat.Since) {
		t.Fatalf("stored seat = %+v", got.Seat)
	}
	back, changed, err := s.SetSeat(ctx, "alpha", Seat{}, nil)
	if err != nil || !changed || back.External {
		t.Fatalf("detach = %+v, %v, %v", back, changed, err)
	}
}

// The hook runs under the lock that serializes post append, with the log end
// it sees, so no post can land between the hook and the new seat.
func TestSetSeatHookRunsUnderThePostLock(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(rec.ID, PostInput{From: "alpha:a", To: []string{"alpha"}, Text: "before"}); err != nil {
		t.Fatal(err)
	}
	dir, _ := s.dir(rec.ID)
	ran := false
	_, changed, err := s.SetSeat(context.Background(), rec.ID, Seat{External: true}, func(end int64) error {
		ran = true
		f, err := os.OpenFile(filepath.Join(dir, "room.lock"), os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Errorf("post lock is free inside the hook: %v", err)
		}
		if size, _ := s.FileSize(rec.ID); size != end {
			t.Errorf("hook end = %d, log size %d", end, size)
		}
		return nil
	})
	if err != nil || !changed || !ran {
		t.Fatalf("SetSeat = %v, %v, ran %v", changed, err, ran)
	}
	if _, changed, _ := s.SetSeat(context.Background(), rec.ID, Seat{External: true}, func(int64) error { t.Error("hook ran without a change"); return nil }); changed {
		t.Fatal("unchanged seat reported a change")
	}
}
