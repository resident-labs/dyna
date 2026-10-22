package engine

import "testing"

func TestMergeServiceHasNoBrowserLandingAction(t *testing.T) {
	if testCalls(t, false)["LandChange"] != nil {
		t.Fatal("browser can still start an independent landing")
	}
}
