package activity

import "time"

// runPublisher coalesces updates and flushes on ticks and shutdown. A renewing
// publisher also writes on quiet ticks; a proof publisher writes only new proof.
func runPublisher[T, U any](ticks <-chan time.Time, updates <-chan U, renew bool, snapshot func(U) (T, bool), prepare func(T, time.Time) T, write func(T) error) {
	var pending T
	var dirty bool
	for {
		var tick time.Time
		var closed bool
		select {
		case update, ok := <-updates:
			if !ok {
				closed = true
			} else {
				pending, closed = snapshot(update)
				dirty = true
			}
		case tick = <-ticks:
			if renew {
				var zero U
				pending, closed = snapshot(zero)
				dirty = true
			}
		}
		if (closed || !tick.IsZero()) && dirty {
			if tick.IsZero() {
				tick = time.Now()
			}
			_ = write(prepare(pending, tick))
			dirty = false
		}
		if closed {
			return
		}
	}
}

// SignalPublisher publishes copier proof without blocking its producer.
type SignalPublisher struct{ updates chan Signal }

func NewSignalPublisher() *SignalPublisher { return &SignalPublisher{updates: make(chan Signal, 1)} }
func (p *SignalPublisher) Send(s Signal) {
	select {
	case p.updates <- s:
	default:
		select {
		case <-p.updates:
		default:
		}
		select {
		case p.updates <- s:
		default:
		}
	}
}
func (p *SignalPublisher) Close() { close(p.updates) }

func (p *SignalPublisher) Run(ticks <-chan time.Time, write func(Signal) error) {
	var storedLease, storedSince time.Time
	// The generic loop receives updates itself, so forward the received signal
	// through a local snapshot instead.
	runPublisher(ticks, p.updates, false, func(s Signal) (Signal, bool) { return s, false }, func(s Signal, _ time.Time) Signal { return s }, func(s Signal) error {
		if s.Copier == CopierRunning {
			proof := s.LeaseUntil.Add(-Lease)
			if storedLease.IsZero() || !proof.Before(storedLease) {
				s.ObservedSince = proof
			} else if s.ObservedSince.Before(storedSince) {
				s.ObservedSince = storedSince
			}
		}
		if err := write(s); err != nil {
			return err
		}
		storedLease, storedSince = s.LeaseUntil, s.ObservedSince
		return nil
	})
}
