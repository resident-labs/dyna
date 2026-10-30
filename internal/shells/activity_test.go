package shells

import (
	"bufio"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/activity"
)

func TestRawOutputActivityBeforeLineBuffer(t *testing.T) {
	now := time.Unix(100, 0)
	var got []activity.Signal
	r := &activityReader{Reader: strings.NewReader("no newline"), now: func() time.Time { return now }, publish: func(s activity.Signal) { got = append(got, s) }}
	r.start()
	b := bufio.NewReaderSize(r, 16)
	if _, err := b.Peek(1); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].LastOutput != now || got[1].Copier != activity.CopierRunning {
		t.Fatalf("activity before newline: %+v", got)
	}
	now = now.Add(time.Second)
	_, _ = io.ReadAll(b)
	r.end(io.EOF)
	if got[len(got)-1].Copier != activity.CopierEnded {
		t.Fatalf("end: %+v", got)
	}
}

func TestFailedPublicationDoesNotRenewLease(t *testing.T) {
	now := time.Unix(100, 0)
	stored := activity.Signal{Copier: activity.CopierRunning, LeaseUntil: now.Add(activity.Lease)}
	p := &activityPublisher{updates: make(chan activity.Signal, 1)}
	p.send(activity.Signal{Copier: activity.CopierRunning, LastOutput: now})
	close(p.updates)
	ticks := make(chan time.Time)
	close(ticks)
	calls := 0
	p.run(ticks, func(s activity.Signal) error { calls++; return errors.New("write failed") })
	if calls != 1 {
		t.Fatalf("writes = %d", calls)
	}
	if stored.At(now.Add(activity.Lease)).Copier != activity.CopierUnknown {
		t.Fatal("expired observation remained known")
	}
}

func TestActivityStoredWithLease(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	signal := activity.Signal{Copier: activity.CopierRunning, LastOutput: now, LeaseUntil: now.Add(activity.Lease)}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.publishActivity(db, meta.ID, signal); err != nil {
		t.Fatal(err)
	}
	got, err := s.Activity(meta.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Copier != signal.Copier || !got.LastOutput.Equal(now) {
		t.Fatalf("got %+v", got)
	}
	got, err = s.Activity(meta.ID, signal.LeaseUntil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Copier != activity.CopierUnknown {
		t.Fatalf("expired: %+v", got)
	}
}
