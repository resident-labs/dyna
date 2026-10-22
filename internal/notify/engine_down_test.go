package notify

import "testing"

// A post queued for a session is only stored while the engine is down. The
// result says so, so `dyna post` does not imply someone received it.
func TestPostReportsEngineDownOnlyForQueuedSessions(t *testing.T) {
	f := newFixture(t)
	owner := Sender{Kind: SenderOwner, Room: f.a.Name, Human: true}
	for _, tc := range []struct {
		name     string
		running  bool
		mentions []string
		down     bool
	}{
		{"session, engine down", false, []string{"@" + f.decoder.Address()}, true},
		{"session, engine up", true, []string{"@" + f.decoder.Address()}, false},
		{"owner only, engine down", false, []string{"@" + f.a.Name}, false},
	} {
		f.svc.EngineRunning = func() bool { return tc.running }
		res, err := f.svc.Post(f.ctx, PostInput{Sender: owner, Mentions: tc.mentions, Text: "status?"})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.EngineDown != tc.down {
			t.Fatalf("%s: EngineDown = %v, want %v (%+v)", tc.name, res.EngineDown, tc.down, res.Deliveries)
		}
	}
}
