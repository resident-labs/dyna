package admission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/agentslice"
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

// Turn memory is the live agents' memory over the number of live agents, taken
// together from one inventory.
func TestSampleAveragesMemoryOverTheLiveAgents(t *testing.T) {
	states := &memState{}
	o, _ := observerFor(fixedSource{r: busy}, 2, states)
	o.AgentMemory = func(context.Context) (uint64, int) { return 16 * gib, 8 }
	if err := o.Sample(t.Context()); err != nil {
		t.Fatal(err)
	}
	if states.st.TurnBytes != 2*gib {
		t.Fatalf("turn bytes = %d, want 16 GiB over 8 agents = 2 GiB", states.st.TurnBytes)
	}
	// With fewer than two agents there is nothing to average: the fallback stays.
	states = &memState{}
	o, _ = observerFor(fixedSource{r: busy}, 2, states)
	o.AgentMemory = func(context.Context) (uint64, int) { return 16 * gib, 1 }
	_ = o.Sample(t.Context())
	if states.st.TurnBytes != 0 {
		t.Fatalf("turn bytes = %d, want the fallback (0) with one agent", states.st.TurnBytes)
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

// fakeRoomCgroups writes a cgroup v2 tree whose user manager runs one
// dyna-room-<id>.slice per entry, beside the noise a real tree has: the
// intermediate slice's own files, a session scope, and a room whose
// pids.current is gone. It returns the root and the /proc/self/cgroup file
// that names the user manager.
func fakeRoomCgroups(t *testing.T, rooms map[string]int) (root, self string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "user.slice", "user-1000.slice", "user@1000.service", "dyna.slice", "dyna-room.slice")
	for _, d := range []string{dir, filepath.Join(dir, "dyna-ses_1.scope"), filepath.Join(dir, "dyna-room-gone.slice")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{
		// The intermediate slice's own count and process list are not rooms.
		"pids.current": "999\n",
		"cgroup.procs": "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, pids := range rooms {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "pids.current"), []byte(strconv.Itoa(pids)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	selfDir := t.TempDir()
	self = filepath.Join(selfDir, "self")
	if err := os.WriteFile(self, []byte("0::/user.slice/user-1000.slice/user@1000.service/dyna.slice/dyna-ses_1.scope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, self
}

// A held sample records what each room's own slice holds: the daemons a
// room's supervisors started outside every session scope, which the
// running-turn count never sees. The noise beside the room slices is not
// counted, and a room whose pids.current is gone is skipped.
func TestSampleRecordsRoomSliceProcesses(t *testing.T) {
	root, self := fakeRoomCgroups(t, map[string]int{
		"dyna-room-roomlimits.slice": 3,
		"dyna-room-other.slice":      5,
	})
	o, seen := observerFor(fixedSource{r: busy}, 6, &memState{})
	o.RoomPids = func(context.Context) RoomPids {
		byRoom, total := agentslice.RoomPidsAt(root, self)
		return RoomPids{ByRoom: byRoom, Total: total}
	}
	if err := o.Sample(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 {
		t.Fatalf("observations = %+v, want one held sample", *seen)
	}
	pids := (*seen)[0].RoomPids
	want := map[string]int{"dyna-room-roomlimits.slice": 3, "dyna-room-other.slice": 5}
	if pids.Total != 8 || !reflect.DeepEqual(pids.ByRoom, want) {
		t.Fatalf("room pids = %+v, want total 8 of %v", pids, want)
	}
}

// Without a cgroup root there are no room counts and no error: the sample
// still records, with zero.
func TestSampleRecordsZeroRoomPidsWithoutACgroupRoot(t *testing.T) {
	selfDir := t.TempDir()
	self := filepath.Join(selfDir, "self")
	if err := os.WriteFile(self, []byte("0::/user.slice/user-1000.slice/user@1000.service/dyna.slice/dyna-ses_1.scope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o, seen := observerFor(fixedSource{r: busy}, 6, &memState{})
	o.RoomPids = func(context.Context) RoomPids {
		byRoom, total := agentslice.RoomPidsAt(filepath.Join(t.TempDir(), "no-such-root"), self)
		return RoomPids{ByRoom: byRoom, Total: total}
	}
	if err := o.Sample(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0].RoomPids.Total != 0 || len((*seen)[0].RoomPids.ByRoom) != 0 {
		t.Fatalf("observations = %+v; want one sample with zero room pids", *seen)
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
