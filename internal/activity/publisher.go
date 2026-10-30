package activity

import (
	"context"
	"dyna-agent/internal/database"
	"sync"
	"time"
)

// Publisher coalesces activity without waiting for SQLite on the producer.
type Publisher struct {
	mu      sync.Mutex
	record  Record
	pending chan struct{}
	done    chan struct{}
	closed  bool
}

func NewPublisher(dataDir string, key Key, initial Record) *Publisher {
	p := &Publisher{record: initial, pending: make(chan struct{}, 1), done: make(chan struct{})}
	go p.run(dataDir, key)
	p.wake()
	return p
}

func (p *Publisher) wake() {
	select {
	case p.pending <- struct{}{}:
	default:
	}
}

// Output records observed bytes without storing their content.
func (p *Publisher) Output() {
	p.mu.Lock()
	if !p.closed {
		p.record.Signal.LastOutput = time.Now()
		p.wake()
	}
	p.mu.Unlock()
}

// Update replaces lifecycle and link data while preserving output activity.
func (p *Publisher) Update(r Record) {
	p.mu.Lock()
	if !p.closed {
		r.Signal.LastOutput = p.record.Signal.LastOutput
		p.record = r
		p.wake()
	}
	p.mu.Unlock()
}

// Close queues a final observation. It does not wait for a database write.
func (p *Publisher) Close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		p.wake()
	}
	p.mu.Unlock()
}

func (p *Publisher) run(dataDir string, key Key) {
	defer close(p.done)
	db, err := database.Open(dataDir)
	if err != nil {
		return
	}
	ticker := time.NewTicker(PublishInterval)
	defer ticker.Stop()
	p.publishLoop(ticker.C, func(r Record) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = Put(ctx, db, key, r)
	})
}

// publishLoop renews the observer lease even when no new output arrives.
func (p *Publisher) publishLoop(ticks <-chan time.Time, write func(Record)) {
	for {
		var tick time.Time
		select {
		case <-p.pending:
		case tick = <-ticks:
		}
		p.mu.Lock()
		closed := p.closed
		r := p.record
		p.mu.Unlock()
		if closed || !tick.IsZero() {
			if tick.IsZero() {
				tick = time.Now()
			}
			r.Signal.LeaseUntil = tick.Add(Lease)
			write(r)
		}
		if closed {
			return
		}
	}
}

// Done closes after the final publication attempt.
func (p *Publisher) Done() <-chan struct{} { return p.done }

// Coverage records whether this turn observes raw bytes or only complete frames.
func (p *Publisher) Coverage(raw bool) {
	p.mu.Lock()
	if !p.closed {
		p.record.Reason = ReasonPartialFrameUnobserved
		p.record.Signal.Copier = CopierUnknown
		if raw {
			p.record.Reason = ReasonNone
			p.record.Signal.Copier = CopierRunning
		}
		p.wake()
	}
	p.mu.Unlock()
}

// End records the turn lifecycle without making incomplete output known.
func (p *Publisher) End() {
	p.mu.Lock()
	if !p.closed {
		p.record.Lifecycle = CopierEnded
		if p.record.Signal.Copier == CopierRunning {
			p.record.Signal.Copier = CopierEnded
		}
		p.closed = true
		p.wake()
	}
	p.mu.Unlock()
}

// FinalFlushWait bounds the shutdown wait after the execution result is saved.
const FinalFlushWait time.Duration = 100 * time.Millisecond

// Wait waits for final publications or the supplied deadline. A timeout never
// makes the old observation fresh; its existing lease still expires.
func Wait(done []<-chan struct{}, deadline <-chan time.Time) bool {
	for _, ch := range done {
		select {
		case <-ch:
		case <-deadline:
			return false
		}
	}
	return true
}

// WaitFinal bounds the wait for final publications.
func WaitFinal(done ...<-chan struct{}) {
	timer := time.NewTimer(FinalFlushWait)
	defer timer.Stop()
	Wait(done, timer.C)
}
