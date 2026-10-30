package activity

import (
	"testing"
	"time"
)

func TestProofRestartsAfterLeaseGap(t *testing.T) {
	first := time.Unix(100, 0)
	var s Signal
	s = s.Prove(first)
	s = s.Prove(first.Add(PublishInterval))
	if !s.ObservedSince.Equal(first) {
		t.Fatalf("continuous: %+v", s)
	}
	resumed := first.Add(2 * Lease)
	s = s.Prove(resumed)
	if !s.ObservedSince.Equal(resumed) || !s.LeaseUntil.Equal(resumed.Add(Lease)) {
		t.Fatalf("resumed: %+v", s)
	}
}
