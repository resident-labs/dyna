package admission

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fixedSource struct {
	r   Readings
	err error
}

func (f fixedSource) Read(context.Context) (Readings, error) { return f.r, f.err }

// stuckSource ignores its context until the test releases it.
type stuckSource struct{ release chan struct{} }

func (s stuckSource) Read(context.Context) (Readings, error) {
	<-s.release
	return Readings{}, nil
}

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

func observerFor(src Source, running int, states *memState) (Observer, *[]Observation) {
	var seen []Observation
	return Observer{
		Source: src, States: states,
		Running: func(context.Context) (int, error) { return running, nil },
		Record:  func(_ context.Context, ob Observation) error { seen = append(seen, ob); return nil },
		Mode:    func() Mode { return ModeObserve },
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
	o.Mode = func() Mode { return ModeOff }
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

func TestSampleMeasuresTurnMemoryFromAgentBytes(t *testing.T) {
	states := &memState{}
	o, _ := observerFor(fixedSource{r: busy}, 4, states)
	o.AgentBytes = func() uint64 { return 8 * gib }
	if err := o.Sample(t.Context()); err != nil {
		t.Fatal(err)
	}
	if states.st.TurnBytes != 2*gib {
		t.Fatalf("turn bytes = %d, want 2 GiB", states.st.TurnBytes)
	}
}

// A read that ignores its context cannot hold the sampler: the bound ends it.
func TestAStuckReadIsAbandonedAtTheBound(t *testing.T) {
	stuck := stuckSource{release: make(chan struct{})}
	t.Cleanup(func() { close(stuck.release) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readBounded(ctx, stuck); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context's", err)
	}
}

func TestRunSamplesAtEachTickAndStopsWithItsContext(t *testing.T) {
	o, seen := observerFor(fixedSource{r: busy}, 6, &memState{})
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { o.Run(ctx, ticks); close(done) }()
	ticks <- time.Time{}
	ticks <- time.Time{} // the second send returns only after the first sample ended
	cancel()
	<-done
	if len(*seen) < 1 {
		t.Fatalf("observations = %+v, want a sample for each tick", *seen)
	}
}
