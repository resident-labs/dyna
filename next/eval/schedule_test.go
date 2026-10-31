package eval

import "testing"

func TestScheduleAndRetry(t *testing.T) {
	seen := map[Cell]bool{}
	for _, cell := range Schedule() {
		if seen[cell] {
			t.Fatalf("duplicate cell: %+v", cell)
		}
		seen[cell] = true
	}
	if len(seen) != 300 {
		t.Fatalf("got %d cells", len(seen))
	}
	if !RetryAllowed(1, true, false) || RetryAllowed(2, true, false) || RetryAllowed(1, true, true) || RetryAllowed(1, false, false) {
		t.Fatal("retry rule changed")
	}
}
