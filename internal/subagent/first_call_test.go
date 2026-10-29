package subagent

import "testing"

// A Pi turn stores its first model call. The fake Pi reports input but no
// cache counts, so the stored cache counts stay unknown (nil), not zero.
func TestRunTurnStoresThePiFirstCall(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	e := newTestEnv(t, piTestProfile("pi-first", "first/provider-model", path, logPath))
	info, _ := e.start("RESPOND: first call", func(so *StartOptions) { so.Profile = "pi-first" })
	e.wait(info.ID, 1)
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if turn.FirstCall == nil || turn.FirstCall.Input == nil || *turn.FirstCall.Input != 10 {
		t.Fatalf("FirstCall = %+v, want input 10", turn.FirstCall)
	}
	if turn.FirstCall.CacheRead != nil || turn.FirstCall.CacheWrite != nil {
		t.Fatalf("FirstCall cache = %+v, want unknown nil", turn.FirstCall)
	}
}
