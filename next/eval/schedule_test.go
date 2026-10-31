package eval

import "testing"

func TestScheduleSkipsLiveTrialUnsafeTasks(t *testing.T) {
	for _, skipRepo := range []bool{false, true} {
		cells := Schedule(skipRepo, false)
		want := 32
		if skipRepo {
			want = 30
		}
		if len(cells) != want {
			t.Fatalf("got %d cells, want %d", len(cells), want)
		}
		seen := map[Cell]bool{}
		for _, c := range cells {
			if seen[c] || c.Provider != Claude || (c.Arm != Caller && c.Arm != Declared) || c.Task == 14 || c.Task == 15 || c.Task == 16 || c.Task == 17 || (skipRepo && c.Task == 2) {
				t.Fatalf("unsafe cell: %+v", c)
			}
			seen[c] = true
		}
	}
	if got := len(Schedule(true, true)); got != 28 {
		t.Fatalf("got %d cells with both optional skips", got)
	}
	if !RetryAllowed(1, true, false) || RetryAllowed(2, true, false) || RetryAllowed(1, true, true) {
		t.Fatal("retry rule changed")
	}
}
