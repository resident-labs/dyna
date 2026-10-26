package admission

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fixedSource struct {
	r   Readings
	err error
}

func (f fixedSource) Read(context.Context) (Readings, error) { return f.r, f.err }

type memState struct {
	st      State
	loadErr error
	saveErr error
}

func (m *memState) Load(context.Context, Pool) (State, error) { return m.st, m.loadErr }
func (m *memState) Save(_ context.Context, _ Pool, st State) error {
	if m.saveErr == nil {
		m.st = st
	}
	return m.saveErr
}

func observerFor(src Source, running int, states *memState) (*Observer, *[]Observation) {
	var seen []Observation
	return &Observer{
		Source: src, States: states,
		Running: func(context.Context) (int, error) { return running, nil },
		Record:  func(_ context.Context, ob Observation) error { seen = append(seen, ob); return nil },
		Mode:    func(context.Context) Mode { return ModeObserve },
		Now:     func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) },
	}, &seen
}

var busy = Readings{Cores: 8, MemTotal: 32 * gib, MemAvail: 4 * gib, PressureCPU: 0, PressureMem: 0}

func TestSampleRecordsOnlyWhenNewTurnsWouldBeHeld(t *testing.T) {
	// 4 GiB free of 32 is under the 3.2 GiB reserve plus one turn: limit is the running count.
	o, seen := observerFor(fixedSource{r: busy}, 6, &memState{})
	if err := o.Sample(t.Context()); err != nil || len(*seen) != 1 || (*seen)[0].Running != 6 || (*seen)[0].Limit != 6 {
		t.Fatalf("observations = %+v, %v", *seen, err)
	}
	o, seen = observerFor(fixedSource{r: Readings{Cores: 8, MemTotal: 32 * gib, MemAvail: 30 * gib, PressureCPU: 0, PressureMem: 0}}, 2, &memState{})
	if err := o.Sample(t.Context()); err != nil || len(*seen) != 0 {
		t.Fatalf("a machine with room recorded %+v, %v", *seen, err)
	}
}

func TestSampleDoesNothingWhenOff(t *testing.T) {
	o, seen := observerFor(fixedSource{r: busy}, 6, &memState{})
	o.Mode = func(context.Context) Mode { return ModeOff }
	if err := o.Sample(t.Context()); err != nil || len(*seen) != 0 {
		t.Fatalf("off recorded %+v, %v", *seen, err)
	}
}

// A state that cannot be loaded or saved skips the sample instead of resetting
// the hysteresis, which would skew the numbers.
func TestSampleSkipsWhenTheStateCannotBeKept(t *testing.T) {
	for name, states := range map[string]*memState{
		"load fails": {loadErr: errors.New("database is locked")},
		"save fails": {saveErr: errors.New("database is locked")},
	} {
		o, seen := observerFor(fixedSource{r: busy}, 6, states)
		if err := o.Sample(t.Context()); err == nil || len(*seen) != 0 {
			t.Errorf("%s: recorded %+v, err %v", name, *seen, err)
		}
	}
}

// The agents group's memory is spread over every agent session that holds a
// process, roots and services included, not only the running turns.
func TestSampleDividesAgentMemoryByEveryAgent(t *testing.T) {
	states := &memState{}
	o, _ := observerFor(fixedSource{r: busy}, 2, states)
	o.Agents = func(context.Context) (int, error) { return 8, nil } // 2 running + idle + roots + services
	o.AgentBytes = func(context.Context) uint64 { return 16 * gib }
	if err := o.Sample(t.Context()); err != nil {
		t.Fatal(err)
	}
	if states.st.TurnBytes != 2*gib {
		t.Fatalf("turn bytes = %d, want 16 GiB over 8 agents = 2 GiB", states.st.TurnBytes)
	}
}

// A panic in a source or a callback is recovered, reported, and skips the
// sample.
func TestSampleRecoversFromAPanic(t *testing.T) {
	o, seen := observerFor(fixedSource{r: busy}, 6, &memState{})
	o.Running = func(context.Context) (int, error) { panic("boom") }
	if err := o.Sample(t.Context()); err == nil || !strings.Contains(err.Error(), "panicked") || len(*seen) != 0 {
		t.Fatalf("err = %v, observations %+v", err, *seen)
	}
	o.Source = panicSource{}
	if err := o.Sample(t.Context()); err == nil {
		t.Fatal("a panicking source did not report")
	}
}

type panicSource struct{}

func (panicSource) Read(context.Context) (Readings, error) { panic("source") }

// blockedRunning blocks every Running call until released, and counts the calls.
type blockedRunning struct {
	release chan struct{}
	started chan struct{}
	calls   atomic.Int32
}

func (b *blockedRunning) Running(context.Context) (int, error) {
	b.calls.Add(1)
	b.started <- struct{}{}
	<-b.release
	return 1, nil
}

// A sample that never ends costs one goroutine: later ticks are skipped, and
// Run returns as soon as its context ends, without waiting for the sample.
func TestARunningSampleSkipsLaterTicksAndNeverBlocksShutdown(t *testing.T) {
	b := &blockedRunning{release: make(chan struct{}), started: make(chan struct{}, 4)}
	t.Cleanup(func() { close(b.release) })
	o, _ := observerFor(fixedSource{r: busy}, 6, &memState{})
	o.Running = b.Running
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { o.Run(ctx, ticks); close(done) }()
	ticks <- time.Time{}
	<-b.started // the first sample is blocked inside Running
	ticks <- time.Time{}
	ticks <- time.Time{} // both skipped: the receive returns only once Run read the tick
	cancel()
	<-done // Run returned although the sample is still blocked
	if got := b.calls.Load(); got != 1 {
		t.Fatalf("Running was called %d times, want 1: ticks must not start a second sample", got)
	}
}

func TestRunSamplesAtEachTick(t *testing.T) {
	o, seen := observerFor(fixedSource{r: busy}, 6, &memState{})
	ended := make(chan struct{})
	o.sampled = func() { ended <- struct{}{} }
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go o.Run(ctx, ticks)
	for i := 0; i < 2; i++ {
		ticks <- time.Time{}
		<-ended // the sample ended and cleared busy, so the next tick starts one
	}
	if len(*seen) != 2 {
		t.Fatalf("observations = %+v, want one per tick", *seen)
	}
}
