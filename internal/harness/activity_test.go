package harness

import "testing"

func TestTurnActivityObservesRawOutputBeforeNewline(t *testing.T) {
	calls := 0
	b := newObservedBuffer(nil, nil)
	b.onBytes = func() { calls++ }
	_, _ = b.Write([]byte("partial"))
	if calls != 1 {
		t.Fatalf("observations = %d", calls)
	}
}
