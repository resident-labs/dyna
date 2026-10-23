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

func TestWakesRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  Message
		want bool
	}{
		{"human post to root", Message{Wake: true, From: "r", Human: true, Kind: "message", To: []string{"r"}}, true},
		{"human post to an agent", Message{Wake: true, From: "r", Human: true, Kind: "message", To: []string{"r:a"}}, false},
		{"root's own post", Message{Wake: true, From: "r", Kind: "message", To: []string{"r"}}, false},
		{"agent post to root", Message{Wake: true, From: "r:a", Kind: "message", To: []string{"r"}}, false},
		{"agent notice to root", Message{Wake: true, From: "r:a", Kind: "notice", To: []string{"r"}}, false},
		{"report owed to the owner", Message{Wake: true, From: "r:a", Kind: "completed", To: []string{"r"}, OwnerReport: true}, false},
		{"failure owed to the owner", Message{Wake: true, From: "r:a", Kind: "failed", To: []string{"r"}, OwnerReport: true}, true},
		{"root's shell result", Message{Wake: true, From: "shell:x", Kind: "completed", To: []string{"r"}, OwnerReport: true}, true},
		{"root's workflow result", Message{From: "run:x", Kind: "completed", To: []string{"r"}, OwnerReport: true}, true},
		{"report for the desktop only", Message{Wake: true, From: "r:b", Kind: "completed", To: []string{"r"}}, false},
		{"failure to a parent", Message{Wake: true, From: "r:b", Kind: "failed", To: []string{"r:a", "r"}}, false},
		{"internal root's report", Message{Wake: true, From: "r:root", Kind: "completed", To: []string{"r"}, OwnerReport: true}, false},
		{"internal root's post", Message{Wake: true, From: "r:root", Kind: "message", To: []string{"r"}}, false},
		{"coordination without wake field", Message{From: "r:a", Kind: "message", To: []string{"r"}}, false},
		{"passive completion", Message{From: "r:a", Kind: "completed", To: []string{"r"}, OwnerReport: true}, false},
		{"failure remains actionable", Message{From: "r:a", Kind: "failed", To: []string{"r"}, OwnerReport: true}, true},
		{"stopped remains actionable", Message{From: "r:a", Kind: "stopped", To: []string{"r"}, OwnerReport: true}, true},
		{"marked action", Message{From: "r:a", Kind: "message", To: []string{"r"}, NeedsRoot: true}, true},
		{"marked notice", Message{From: "r:a", Kind: "notice", To: []string{"r"}, NeedsRoot: true}, true},
		{"direct copy", Message{From: "r:a", Kind: "message", To: []string{"r:root"}}, false},
		{"direct action", Message{From: "r:a", Kind: "message", To: []string{"r:root"}, NeedsRoot: true}, true},
		{"alert", Message{From: "r:a", Kind: "alert", To: []string{"r"}}, true},
		{"voice request", Message{From: "r:voice-call", Kind: "message", To: []string{"r"}}, true},
		{"unmarked PR notice", Message{From: "r:a", Kind: "pr", To: []string{"r"}}, false},
		{"merge failure", Message{From: "r:a", Kind: "pr", To: []string{"r"}, NeedsRoot: true}, true},
		{"goal record", Message{Wake: true, From: "r:a", Kind: "goal", To: []string{"r"}}, false},
	} {
		if got := WakesRoot(tc.msg, "r"); got != tc.want {
			t.Errorf("%s: WakesRoot = %v, want %v", tc.name, got, tc.want)
		}
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
