package admission

import (
	"context"
	"testing"
	"time"
)

// Relief stops the longest-idle processes first, up to the sample bound, and
// leaves a busy session and a process idle under the minimum.
func TestReliefStopsLongestIdleFirstAndSkipsBusyAndRecent(t *testing.T) {
	list := []Idle[string]{
		{Handle: "recent", Session: "s-recent", Idle: 4 * time.Minute},
		{Handle: "old", Session: "s-old", Idle: 2 * time.Hour},
		{Handle: "busy", Session: "s-busy", Idle: 3 * time.Hour},
		{Handle: "mid", Session: "s-mid", Idle: 30 * time.Minute},
		{Handle: "mid2", Session: "s-mid2", Idle: 20 * time.Minute},
		{Handle: "mid3", Session: "s-mid3", Idle: 10 * time.Minute},
	}
	var stopped []string
	r := Relief[string]{
		List: func(context.Context) ([]Idle[string], error) { return list, nil },
		Busy: func(s string) bool { return s == "s-busy" },
		Unload: func(_ context.Context, p Idle[string], minIdle time.Duration) (bool, error) {
			if minIdle != MinUnloadIdle {
				t.Fatalf("minimum passed to the host: %v", minIdle)
			}
			stopped = append(stopped, p.Handle)
			return true, nil
		},
	}
	if n := r.Run(t.Context()); n != MaxUnloadsPerSample || len(stopped) != 3 || stopped[0] != "old" || stopped[1] != "mid" || stopped[2] != "mid2" {
		t.Fatalf("stopped %d: %v", n, stopped)
	}
}

// Relief runs when memory is short, and not when the machine has room.
func TestSampleRelievesOnlyWhenMemoryIsShort(t *testing.T) {
	for name, tc := range map[string]struct {
		r    Readings
		want int
	}{
		"short":    {Readings{Cores: 8, MemTotal: 32 * gib, MemAvail: 2 * gib}, 1},
		"stalls":   {Readings{Cores: 8, MemTotal: 32 * gib, MemAvail: 30 * gib, PressureMem: EnterMemPressure}, 1},
		"has room": {Readings{Cores: 8, MemTotal: 32 * gib, MemAvail: 30 * gib}, 0},
	} {
		o, _ := observerFor(fixedSource{r: tc.r}, 1, &memState{})
		calls := 0
		o.Relieve = func(context.Context) int { calls++; return 0 }
		if err := o.Sample(t.Context()); err != nil || calls != tc.want {
			t.Errorf("%s: relief ran %d times, err %v", name, calls, err)
		}
	}
	o, _ := observerFor(fixedSource{r: busy}, 1, &memState{})
	o.Mode = func(context.Context) Mode { return ModeOff }
	o.Relieve = func(context.Context) int { t.Fatal("relief ran with the watcher off"); return 0 }
	_ = o.Sample(t.Context())
}
