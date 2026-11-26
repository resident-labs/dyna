package live

import "testing"

// A stop asked while no turn runs is sent once.
func TestChannelStopIsTakenOnce(t *testing.T) {
	c := NewChannel(func() {})
	if c.TakeStop() {
		t.Fatal("a stop nobody asked for")
	}
	c.AskStop()
	if !c.TakeStop() || c.TakeStop() {
		t.Fatal("a stop is sent exactly once")
	}
}
