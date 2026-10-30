package podman

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// seqRunner answers each probe with the next body. Every tick in a test consumes one body.
// It closes drained when it returns the last body, so the driver cancels only after the final probe.
type seqRunner struct {
	bodies  []string
	drained chan struct{}
}

func (r *seqRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "-O" || a == "-M" {
			return nil, nil
		}
	}
	body := r.bodies[0]
	r.bodies = r.bodies[1:]
	if len(r.bodies) == 0 {
		close(r.drained)
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d 000\n", body, len(body))), nil
}

type pressureReport struct {
	slice string
	kind  contract.PressureKind
}

// driveHeartbeat runs one probe for each tick time and returns the pressure reports in order.
func driveHeartbeat(t *testing.T, bodies []string, at []time.Time) []pressureReport {
	t.Helper()
	runner := &seqRunner{bodies: bodies, drained: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", runner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	done := make(chan struct{})
	var got []pressureReport
	go func() {
		defer close(done)
		h.Heartbeat(ctx, ticks, func(bool) { t.Error("reachability changed") }, func(slice string, kind contract.PressureKind) {
			got = append(got, pressureReport{slice, kind})
		})
	}()
	for _, now := range at {
		ticks <- now
	}
	<-runner.drained
	cancel()
	<-done
	return got
}

// tickTimes returns n ticks spaced ten seconds apart.
func tickTimes(n int) []time.Time {
	at := make([]time.Time, n)
	for i := range at {
		at[i] = time.Unix(1000+int64(10*i), 0)
	}
	return at
}

const (
	slicePathA = "/sys/fs/cgroup/dyna.slice/dyna-a.slice/memory.events.local"
	slicePathB = "/sys/fs/cgroup/dyna.slice/dyna-b.slice/memory.events.local"
)

// memoryEvents formats the counters of one memory.events.local file, as the host script prints them.
func memoryEvents(path string, high, max, oom int64) string {
	return fmt.Sprintf("%s:high %d\n%s:max %d\n%s:oom_kill %d\n", path, high, path, max, path, oom)
}

func sliceBody(high, max, oom int64) string {
	return memoryEvents(slicePathA, high, max, oom)
}

func TestHeartbeatFirstSampleOnlySetsBaseline(t *testing.T) {
	body := memoryEvents(slicePathA, 9, 4, 2) + memoryEvents(slicePathB, 9, 4, 2)
	if got := driveHeartbeat(t, []string{body}, tickTimes(1)); len(got) != 0 {
		t.Fatalf("reports = %+v, want none on the baseline read", got)
	}
}

func TestHeartbeatChildRiseReportsOnlyChild(t *testing.T) {
	// A child cgroup's own counters rise. Its parent's memory.events.local stays flat, so only the child reports.
	parent := slicePathA
	child := "/sys/fs/cgroup/dyna.slice/dyna-a.slice/dyna-a-b.slice/memory.events.local"
	bodies := []string{
		memoryEvents(parent, 0, 0, 0) + memoryEvents(child, 0, 0, 0),
		memoryEvents(parent, 0, 0, 0) + memoryEvents(child, 0, 1, 0),
	}
	got := driveHeartbeat(t, bodies, tickTimes(2))
	want := pressureReport{"dyna-a-b.slice", contract.PressureMax}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("reports = %+v, want only %+v", got, want)
	}
}

func TestHeartbeatNoSlicesIsNotAMiss(t *testing.T) {
	// Three empty probes would report unreachable after three misses. The driver fails the test on any reachability change.
	driveHeartbeat(t, []string{"", "", ""}, tickTimes(3))
}

func TestHeartbeatIgnoresContainerScopes(t *testing.T) {
	// A container scope under a box slice hits its own max. Only slice files count, so nothing reports.
	scope := "/sys/fs/cgroup/dyna.slice/dyna-a.slice/libpod-x.scope/memory.events.local"
	bodies := []string{
		memoryEvents(scope, 0, 0, 0),
		memoryEvents(scope, 0, 1, 0),
	}
	if got := driveHeartbeat(t, bodies, tickTimes(2)); len(got) != 0 {
		t.Fatalf("reports = %+v, want none for a scope", got)
	}
}

func TestHeartbeatBadSliceOutputIsNotAMiss(t *testing.T) {
	// The host answers, but a counter is not a number. Three such probes must not report unreachable.
	bad := slicePathA + ":high x\n"
	driveHeartbeat(t, []string{bad, bad, bad}, tickTimes(3))
}

func TestHeartbeatFallenCounterResetsBaseline(t *testing.T) {
	// The cgroup was created again: max fell at tick 0 and rose at tick 1. That sample only sets the baseline.
	// A later max rise, 80 s on, is a new episode and reports once.
	bodies := []string{sliceBody(9, 4, 0)}
	for range 8 {
		bodies = append(bodies, sliceBody(2, 5, 0))
	}
	bodies = append(bodies, sliceBody(2, 6, 0))
	got := driveHeartbeat(t, bodies, tickTimes(len(bodies)))
	want := pressureReport{"dyna-a.slice", contract.PressureMax}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("reports = %+v, want only %+v", got, want)
	}
}

func TestHeartbeatMissingSliceResetsBaseline(t *testing.T) {
	// Slice A is gone for one probe. When it returns with a higher max, that sample only sets the baseline.
	bodies := []string{sliceBody(0, 4, 0), memoryEvents(slicePathB, 0, 0, 0), sliceBody(0, 5, 0)}
	if got := driveHeartbeat(t, bodies, tickTimes(len(bodies))); len(got) != 0 {
		t.Fatalf("reports = %+v, want none after the slice returns", got)
	}
}

func TestHeartbeatEpisodes(t *testing.T) {
	cases := []struct {
		name   string
		ticks  int
		counts func(int) (high, max, oom int64)
		want   int
	}{
		{"sixty second high rise reports once", 9, func(i int) (int64, int64, int64) { return int64(i), 0, 0 }, 1},
		{"short high rise does not report", 12, func(i int) (int64, int64, int64) { return int64(min(i, 5)), 0, 0 }, 0},
		{"second high episode after a quiet minute", 22, func(i int) (int64, int64, int64) {
			high := min(i, 7)
			if i > 13 {
				high += i - 13
			}
			return int64(high), 0, 0
		}, 2},
		{"max rise reports once per episode", 21, func(i int) (int64, int64, int64) {
			switch {
			case i == 20:
				return 0, 3, 0
			case i > 2:
				return 0, 2, 0
			}
			return 0, int64(i), 0
		}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bodies := make([]string, c.ticks)
			for i := range bodies {
				high, max, oom := c.counts(i)
				bodies[i] = sliceBody(high, max, oom)
			}
			if got := driveHeartbeat(t, bodies, tickTimes(c.ticks)); len(got) != c.want {
				t.Fatalf("got %d reports, want %d: %+v", len(got), c.want, got)
			}
		})
	}
}
