package box

import "testing"

func TestTakesRunnersIgnoresRoomAndFollowsTheRule(t *testing.T) {
	h := Host{Name: "pve", State: HostReady, CI: true}
	if !h.TakesRunners(nil) || !h.TakesRunners([]string{"pve"}) || h.TakesRunners([]string{"other"}) {
		t.Fatal("rule not followed")
	}
	h.CI = false
	if h.TakesRunners(nil) {
		t.Fatal("ci off took runners")
	}
	h.CI, h.State = true, HostDraining
	if h.TakesRunners(nil) {
		t.Fatal("draining host took runners")
	}
}
