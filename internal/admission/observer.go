package admission

import (
	"context"
	"log"
	"time"
)

// StateKeeper saves the watcher's state between samples.
type StateKeeper interface {
	Load(ctx context.Context, pool Pool) (State, error)
	Save(ctx context.Context, pool Pool, st State) error
}

// Pool names the state row. The watcher has one.
type Pool string

// Watch is the one state row of the engine's watcher.
const Watch Pool = "watch"

// sampleBound bounds one sample's reads and database calls.
const sampleBound = 10 * time.Second

// Observer is the engine's background sampler. Every SampleEvery it reads the
// machine, counts the running turns, and records when a limit on them would
// have held new turns. It runs only in the engine and is called by no turn, so
// it cannot delay or fail one.
type Observer struct {
	Source Source
	States StateKeeper
	// Running counts the running agent turns, roots and services excluded.
	Running func(ctx context.Context) (int, error)
	// AgentBytes is the memory all agent processes use together, or 0 when it is
	// not known. With two or more running turns it gives the memory of one turn.
	AgentBytes func() uint64
	// Record stores one observation.
	Record func(ctx context.Context, ob Observation) error
	// Mode reads the owner's setting at each sample.
	Mode func() Mode
	Now  func() time.Time
}

// Sample takes one sample. It returns an error only to report; nothing depends
// on it.
func (o Observer) Sample(ctx context.Context) error {
	if o.Mode() != ModeObserve || o.Source == nil {
		return nil
	}
	bound, cancel := context.WithTimeout(ctx, sampleBound)
	defer cancel()
	readings, err := readBounded(bound, o.Source)
	if err != nil {
		return err
	}
	running, err := o.Running(bound)
	if err != nil {
		return err
	}
	// A state that cannot be read would reset the hysteresis to calm and skew the
	// numbers, so the sample is skipped.
	st, err := o.States.Load(bound, Watch)
	if err != nil {
		return err
	}
	now := o.Now()
	if o.AgentBytes != nil && running >= 2 && now.Sub(st.MeasuredAt) >= time.Minute {
		if total := o.AgentBytes(); total > 0 {
			st.TurnBytes = min(max(total/uint64(running), 128<<20), 16<<30)
			st.MeasuredAt = now
		}
	}
	limit, st := Decide(readings, running, st)
	if err := o.States.Save(bound, Watch, st); err != nil {
		return err // without the saved state the next sample would forget the overload
	}
	if running < limit {
		return nil
	}
	return o.Record(bound, Observation{At: now, Running: running, Limit: limit})
}

// Run samples at every tick until ctx ends. The ticks are a parameter so a test
// drives it without a clock.
func (o Observer) Run(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := o.Sample(ctx); err != nil && ctx.Err() == nil {
				log.Printf("admission: sample: %v", err)
			}
		}
	}
}

// readBounded reads a source but gives up when ctx ends, even if the read
// itself ignores its context. A read that stays stuck is abandoned, and the
// sampler goes on at the next tick.
func readBounded(ctx context.Context, src Source) (Readings, error) {
	type result struct {
		r   Readings
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := src.Read(ctx)
		done <- result{r, err}
	}()
	select {
	case res := <-done:
		return res.r, res.err
	case <-ctx.Done():
		return Readings{}, ctx.Err()
	}
}
