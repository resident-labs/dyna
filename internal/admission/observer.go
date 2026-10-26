package admission

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
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
// machine, counts the agents, and records when a limit on running turns would
// have held new ones. It runs only in the engine and is called by no turn, so
// it cannot delay or fail one.
//
// A sample runs in its own goroutine, and only one at a time: a tick that
// finds the last sample still running is skipped, so a blocked read or callback
// costs one goroutine, never a pile. Run does not wait for a sample, so engine
// shutdown never waits on one.
type Observer struct {
	Source Source
	States StateKeeper
	// Running counts the running agent turns, roots and services excluded.
	Running func(ctx context.Context) (int, error)
	// AgentMemory returns the memory of the agent sessions that have live
	// processes and how many they are, both from one inventory, so the average
	// is exact. Without a reliable count it returns 0 agents and the fallback
	// stays.
	AgentMemory func(ctx context.Context) (bytes uint64, agents int)
	// Record stores one observation.
	Record func(ctx context.Context, ob Observation) error
	// Mode reads the owner's setting at each sample.
	Mode func(ctx context.Context) Mode
	Now  func() time.Time

	busy atomic.Bool
	// sampled, when set, runs after a sample ended and the next tick may start
	// one. A test uses it to wait for exactly one sample.
	sampled func()
}

// Sample takes one sample and returns an error only to report; nothing depends
// on it. A panic in a source or callback is recovered and reported as an error.
func (o *Observer) Sample(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sample panicked: %v", r)
		}
	}()
	bound, cancel := context.WithTimeout(ctx, sampleBound)
	defer cancel()
	if o.Mode(bound) != ModeObserve || o.Source == nil {
		return nil
	}
	readings, err := o.Source.Read(bound)
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
	if o.AgentMemory != nil && now.Sub(st.MeasuredAt) >= time.Minute {
		if total, agents := o.AgentMemory(bound); agents >= 2 && total > 0 {
			st.TurnBytes = min(max(total/uint64(agents), 128<<20), 16<<30)
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

// Run starts a sample at each tick until ctx ends, and returns at once when it
// does. The ticks are a parameter so a test drives it without a clock.
func (o *Observer) Run(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if !o.busy.CompareAndSwap(false, true) {
				log.Printf("admission: the last sample is still running; skipping this tick")
				continue
			}
			go func() {
				if err := o.Sample(ctx); err != nil && ctx.Err() == nil {
					log.Printf("admission: sample: %v", err)
				}
				o.busy.Store(false)
				if o.sampled != nil {
					o.sampled()
				}
			}()
		}
	}
}
