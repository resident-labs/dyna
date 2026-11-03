package contract

import "testing"

// A host takes CI runners unless it is the machine the engine runs on, and an explicit choice wins.
func TestHostAddCIDefault(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		ci         *bool
		engineHost bool
		want       bool
	}{{nil, false, true}, {nil, true, false}, {&on, true, true}, {&off, false, false}} {
		if got := (HostAddRequest{CI: tc.ci}).CIOn(tc.engineHost); got != tc.want {
			t.Errorf("ci %v, engine host %v: got %v", tc.ci, tc.engineHost, got)
		}
	}
}
