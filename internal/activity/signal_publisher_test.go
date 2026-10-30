package activity

import (
	"errors"
	"testing"
	"time"
)

func TestFailedPublicationDoesNotRenewLease(t *testing.T) {
	now := time.Unix(100, 0)
	stored := Signal{Copier: CopierRunning, LeaseUntil: now.Add(Lease)}
	p := NewSignalPublisher()
	p.Send(Signal{Copier: CopierRunning, LastOutput: now})
	p.Close()
	ticks := make(chan time.Time)
	close(ticks)
	calls := 0
	p.Run(ticks, func(s Signal) error { calls++; return errors.New("write failed") })
	if calls != 1 {
		t.Fatalf("writes = %d", calls)
	}
	if stored.At(now.Add(Lease)).Copier != CopierUnknown {
		t.Fatal("expired observation remained known")
	}
}

func TestPublisherRestartsCoverageAfterFailedWrites(t *testing.T) {
	p := &SignalPublisher{updates: make(chan Signal)}
	ticks := make(chan time.Time)
	writes := make(chan Signal)
	responses := make(chan error)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ticks, func(s Signal) error { writes <- s; return <-responses })
	}()
	start := time.Unix(100, 0)
	var signal Signal
	signal = signal.Prove(start)
	p.updates <- signal
	ticks <- start
	first := <-writes
	responses <- nil
	signal = signal.Prove(start.Add(PublishInterval))
	p.updates <- signal
	ticks <- start.Add(PublishInterval)
	<-writes
	responses <- errors.New("write failed")
	signal = signal.Prove(start.Add(2 * Lease))
	// The reader continued proving coverage while database writes failed.
	signal.ObservedSince = start
	p.updates <- signal
	ticks <- start.Add(2 * Lease)
	resumed := <-writes
	responses <- nil
	signal = signal.Prove(start.Add(2*Lease + PublishInterval))
	p.updates <- signal
	ticks <- start.Add(2*Lease + PublishInterval)
	continued := <-writes
	responses <- nil
	p.Close()
	<-done
	if !first.ObservedSince.Equal(start) || !resumed.ObservedSince.Equal(start.Add(2*Lease)) || !continued.ObservedSince.Equal(resumed.ObservedSince) {
		t.Fatalf("first = %+v, resumed = %+v, continued = %+v", first, resumed, continued)
	}
}
