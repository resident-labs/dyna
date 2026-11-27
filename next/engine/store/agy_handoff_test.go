package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type handoffRig struct {
	t   *testing.T
	s   *Store
	ctx context.Context
	now time.Time
	n   int
	// profile is the session's profile as the caller last read it.
	profile contract.ProfileName
}

func newHandoffRig(t *testing.T) *handoffRig {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	return &handoffRig{t: t, s: s, ctx: context.Background(), now: time.Unix(1000, 0), profile: "p"}
}

func (r *handoffRig) owner(text string) {
	r.t.Helper()
	r.n++
	post := contract.Event{ID: contract.EventID("post" + string(rune('a'+r.n))), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: text}}
	to := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
	if _, err := r.s.Post(r.ctx, post, []contract.Delivery{{To: to, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *handoffRig) start(turn contract.TurnID) contract.TurnID {
	r.t.Helper()
	started := contract.Event{ID: contract.EventID("start" + string(turn)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	got, _, _, err := r.s.Dispatch(r.ctx, "s", turn, started, false)
	if err != nil {
		r.t.Fatal(err)
	}
	return got
}

func (r *handoffRig) end(turn contract.TurnID, out contract.TurnOutputRequest) {
	r.t.Helper()
	out.Turn = turn
	ended := contract.Event{ID: contract.EventID("end" + string(turn)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Body: contract.TurnEnded{Session: "s", Turn: turn, Outcome: out.Outcome, Cause: out.Cause}}
	if err := r.s.EndTurn(r.ctx, "s", out, ended); err != nil {
		r.t.Fatal(err)
	}
}

func (r *handoffRig) switchTo(profile contract.ProfileName, harness *HarnessSwitch) error {
	event := contract.Event{ID: contract.EventID("switch" + string(profile)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.SessionProfileChanged{Session: "s", Profile: profile}}
	err := r.s.SetSessionProfile(r.ctx, "s", r.profile, profile, event, harness)
	if err == nil {
		r.profile = profile
	}
	return err
}

func (r *handoffRig) handoff() int64 {
	r.t.Helper()
	session, err := r.s.Session(r.ctx, "s")
	if err != nil {
		r.t.Fatal(err)
	}
	return session.HandoffGen
}

// A turn that was already running when a handoff became due has not carried it: only a turn that starts afterwards clears it.
func TestHandoffOutlastsTheTurnRunningWhenItWasSet(t *testing.T) {
	r := newHandoffRig(t)
	r.owner("work")
	r.start("t1")
	if err := r.switchTo("q", &HarnessSwitch{}); err != nil {
		t.Fatal(err)
	}
	if r.handoff() == 0 {
		t.Fatal("the switch left no handoff")
	}
	r.end("t1", contract.TurnOutputRequest{Outcome: contract.TurnCompleted})
	if r.handoff() == 0 {
		t.Fatal("the turn that was running carried the handoff")
	}
	r.owner("next")
	r.now = r.now.Add(time.Second)
	// The box restarts for the new harness; it says hello, and the turn that starts then carries the handoff.
	if err := r.s.NoteBoxHello(r.ctx, "b", r.handoff(), "agy", true); err != nil {
		t.Fatal(err)
	}
	r.start("t2")
	r.end("t2", contract.TurnOutputRequest{Outcome: contract.TurnCompleted})
	if r.handoff() != 0 {
		t.Fatalf("the next turn left the handoff: %d", r.handoff())
	}
}

// A session that is closing takes no harness switch, checked in the same write.
func TestHarnessSwitchRefusesAClosingSession(t *testing.T) {
	r := newHandoffRig(t)
	if _, err := r.s.writer.Exec("UPDATE sessions SET state='closing' WHERE id='s'"); err != nil {
		t.Fatal(err)
	}
	if err := r.switchTo("g", &HarnessSwitch{}); err != ErrSessionClosing {
		t.Fatalf("err = %v", err)
	}
	if r.handoff() != 0 {
		t.Fatal("handoff set")
	}
}

// A turn records the harness of the box it was dispatched on, so a switch while it runs does not change which harness served it.
func TestTurnKeepsTheHarnessItWasDispatchedOn(t *testing.T) {
	r := newHandoffRig(t)
	if _, err := r.s.writer.Exec("UPDATE boxes SET harness='pig' WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	r.owner("work")
	r.start("t1")
	if err := r.switchTo("q", &HarnessSwitch{}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.s.TurnHarness(r.ctx, "s", "t1"); err != nil || got != "pig" {
		t.Fatalf("turn harness %q: %v", got, err)
	}
}

// The write that opens a turn checks the fence itself: a box that has not started since the switch gets none, even when the
// read that preceded it said otherwise; one that started after does.
func TestDispatchHonoursTheFenceInItsOwnWrite(t *testing.T) {
	r := newHandoffRig(t)
	r.owner("work")
	if err := r.switchTo("q", &HarnessSwitch{}); err != nil {
		t.Fatal(err)
	}
	if got := r.start("t1"); got != "" {
		t.Fatalf("the old box was given turn %q", got)
	}
	if fence, err := r.s.HandoffFence(r.ctx, "s"); err != nil || fence == 0 {
		t.Fatalf("fence %d: %v", fence, err)
	}
	if err := r.s.NoteBoxHello(r.ctx, "b", r.handoff(), "agy", true); err != nil {
		t.Fatal(err)
	}
	if fence, err := r.s.HandoffFence(r.ctx, "s"); err != nil || fence != 0 {
		t.Fatalf("fence after the hello %d: %v", fence, err)
	}
	if got := r.start("t1"); got != "t1" {
		t.Fatalf("the restarted box got turn %q", got)
	}
}

// A switch decided from a profile the session no longer has is refused in the write, so two overlapping requests cannot apply a
// stale decision after the other has switched.
func TestHarnessSwitchChecksTheProfileItDecidedFrom(t *testing.T) {
	r := newHandoffRig(t)
	event := contract.Event{ID: "stale", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.SessionProfileChanged{Session: "s", Profile: "g"}}
	if err := r.s.SetSessionProfile(r.ctx, "s", "old", "g", event, &HarnessSwitch{}); err != ErrProfileChanged {
		t.Fatalf("err = %v", err)
	}
	if r.handoff() != 0 {
		t.Fatal("a stale decision left a handoff")
	}
}

// The box's ability to hand off is read in the same write as the switch.
func TestHarnessSwitchChecksTheBoxInItsOwnWrite(t *testing.T) {
	r := newHandoffRig(t)
	if err := r.switchTo("g", &HarnessSwitch{RequireHandoff: true}); err != ErrNoHandoff {
		t.Fatalf("err = %v", err)
	}
	if err := r.s.NoteBoxHello(r.ctx, "b", 0, "pig", true); err != nil {
		t.Fatal(err)
	}
	if err := r.switchTo("g", &HarnessSwitch{RequireHandoff: true}); err != nil {
		t.Fatalf("err = %v", err)
	}
}

// A harness switch does not post a waking event while a wake is scheduled (a usage limit's reset): that wake comes at its time.
// With none scheduled, a failed session wakes to carry its input to the new profile.
func TestHarnessSwitchLeavesAScheduledWakeAlone(t *testing.T) {
	wakes := func(r *handoffRig) int {
		var n int
		if err := r.s.Read().QueryRowContext(r.ctx, "SELECT count(*) FROM deliveries WHERE to_id='s' AND state='pending' AND wake=1").Scan(&n); err != nil {
			r.t.Fatal(err)
		}
		return n
	}
	for _, c := range []struct {
		name  string
		out   contract.TurnOutputRequest
		wakes int
	}{
		{"scheduled", contract.TurnOutputRequest{Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: 3600, OneWake: true}, 0},
		{"none scheduled", contract.TurnOutputRequest{Outcome: contract.TurnFailed, Cause: contract.CauseHarnessFailed, Error: "boom"}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newHandoffRig(t)
			r.owner("work")
			r.start("t1")
			r.end("t1", c.out)
			before := wakes(r)
			if err := r.switchTo("q", &HarnessSwitch{}); err != nil {
				t.Fatal(err)
			}
			if got := wakes(r) - before; got != c.wakes {
				t.Fatalf("the switch added %d waking inputs, want %d", got, c.wakes)
			}
		})
	}
}
