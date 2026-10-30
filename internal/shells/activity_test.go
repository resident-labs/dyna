package shells

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/activity"
)

func TestRawOutputActivityBeforeLineBuffer(t *testing.T) {
	now := time.Unix(100, 0)
	var got []activity.Signal
	r := &activityReader{Reader: strings.NewReader("no newline"), now: func() time.Time { return now }, deadline: func(time.Time) error { return nil }, publish: func(s activity.Signal) { got = append(got, s) }}
	r.start()
	if got[0].CPU != activity.CPUUnsupported {
		t.Fatalf("CPU state = %q", got[0].CPU)
	}
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
	initial, err := s.Activity(meta.ID, now)
	if err != nil || initial.CPU != activity.CPUUnknown {
		t.Fatalf("unpublished activity = %+v, %v", initial, err)
	}
	signal := activity.Signal{Copier: activity.CopierRunning, CPU: activity.CPUUnsupported, LastOutput: now, LeaseUntil: now.Add(activity.Lease)}
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
	if got.Copier != signal.Copier || got.CPU != activity.CPUUnsupported || !got.LastOutput.Equal(now) {
		t.Fatalf("got %+v", got)
	}
	got, err = s.Activity(meta.ID, signal.LeaseUntil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Copier != activity.CopierUnknown || got.CPU != activity.CPUUnknown {
		t.Fatalf("expired: %+v", got)
	}
}

type deadlineProofReader struct {
	now   *time.Time
	calls int
}

func (r *deadlineProofReader) Read(p []byte) (int, error) {
	r.calls++
	if r.calls == 1 {
		*r.now = r.now.Add(activity.PublishInterval)
		return 0, os.ErrDeadlineExceeded
	}
	*r.now = r.now.Add(2 * activity.Lease)
	p[0] = 'x'
	return 1, nil
}

func TestCopierProofRestartsAfterPause(t *testing.T) {
	now := time.Unix(100, 0)
	var signals []activity.Signal
	source := &deadlineProofReader{now: &now}
	observer := &activityReader{Reader: source, now: func() time.Time { return now }, deadline: func(time.Time) error { return nil }, publish: func(s activity.Signal) { signals = append(signals, s) }}
	observer.start()
	n, err := observer.Read(make([]byte, 1))
	if n != 1 || err != nil || len(signals) != 3 {
		t.Fatalf("read %d, %v, signals = %+v", n, err, signals)
	}
	first, resumed := signals[1], signals[2]
	if !first.ObservedSince.Equal(time.Unix(101, 0)) || !resumed.ObservedSince.Equal(now) || !resumed.LastOutput.Equal(now) {
		t.Fatalf("first = %+v; resumed = %+v", first, resumed)
	}
}

func TestPublisherRestartsCoverageAfterFailedWrites(t *testing.T) {
	p := &activityPublisher{updates: make(chan activity.Signal)}
	ticks := make(chan time.Time)
	writes := make(chan activity.Signal)
	responses := make(chan error)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.run(ticks, func(s activity.Signal) error { writes <- s; return <-responses })
	}()
	start := time.Unix(100, 0)
	var signal activity.Signal
	signal = signal.Prove(start)
	p.updates <- signal
	ticks <- start
	first := <-writes
	responses <- nil
	signal = signal.Prove(start.Add(activity.PublishInterval))
	p.updates <- signal
	ticks <- start.Add(activity.PublishInterval)
	<-writes
	responses <- errors.New("write failed")
	signal = signal.Prove(start.Add(2 * activity.Lease))
	// The reader continued proving coverage while database writes failed.
	signal.ObservedSince = start
	p.updates <- signal
	ticks <- start.Add(2 * activity.Lease)
	resumed := <-writes
	responses <- nil
	signal = signal.Prove(start.Add(2*activity.Lease + activity.PublishInterval))
	p.updates <- signal
	ticks <- start.Add(2*activity.Lease + activity.PublishInterval)
	continued := <-writes
	responses <- nil
	close(p.updates)
	<-done
	if !first.ObservedSince.Equal(start) || !resumed.ObservedSince.Equal(start.Add(2*activity.Lease)) || !continued.ObservedSince.Equal(resumed.ObservedSince) {
		t.Fatalf("first = %+v, resumed = %+v, continued = %+v", first, resumed, continued)
	}
}
