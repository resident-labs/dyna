package doctor

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

func TestMemoryShort(t *testing.T) {
	for _, tc := range []struct {
		r    Readings
		want bool
	}{
		{Readings{MemTotal: 32 << 30, MemAvail: 2 << 30}, true},
		{Readings{MemTotal: 32 << 30, MemAvail: 30 << 30, PressureMem: 10}, true},
		{Readings{MemTotal: 32 << 30, MemAvail: 30 << 30}, false},
	} {
		if got := MemoryShort(tc.r); got != tc.want {
			t.Errorf("MemoryShort(%+v) = %v", tc.r, got)
		}
	}
}

type fixedMemory Readings

func (f fixedMemory) Read(context.Context) (Readings, error) { return Readings(f), nil }
func TestReliefCheckOnlyListsIdleWhenMemoryIsShort(t *testing.T) {
	calls := 0
	r := Relief[string]{List: func(context.Context) ([]Idle[string], error) { calls++; return nil, nil }}
	for _, reading := range []Readings{{MemTotal: 32 << 30, MemAvail: 30 << 30}, {MemTotal: 32 << 30, MemAvail: 2 << 30}} {
		if err := r.Check(t.Context(), fixedMemory(reading)); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("idle list called %d times; want once", calls)
	}
}
