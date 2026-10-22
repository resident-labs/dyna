package runtime

import "testing"

// TestAdaptersShareLeaseUnlessExclusive keeps sharing the default. Only the
// harnesses listed here may serialize turns, and each one must name its reason.
func TestAdaptersShareLeaseUnlessExclusive(t *testing.T) {
	exclusive := map[string]bool{"antigravity": true}
	for _, a := range RuntimeAdapters() {
		if a.Exclusive != exclusive[a.Harness] {
			t.Errorf("%s Exclusive = %v; only the listed harnesses serialize turns", a.Harness, a.Exclusive)
		}
		if a.Exclusive && a.ExclusiveReason == "" {
			t.Errorf("%s is exclusive without an ExclusiveReason", a.Harness)
		}
		if !a.Exclusive && a.ExclusiveReason != "" {
			t.Errorf("%s shares the lease but sets an ExclusiveReason", a.Harness)
		}
	}
}
