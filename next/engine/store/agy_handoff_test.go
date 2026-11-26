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
	return &handoffRig{t: t, s: s, ctx: context.Background(), now: time.Unix(1000, 0)}
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
	return r.s.SetSessionProfile(r.ctx, "s", profile, event, harness)
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
	if err := r.s.NoteBoxHello(r.ctx, "b", true); err != nil {
		t.Fatal(err)
	}
	r.start("t2")
	r.end("t2", contract.TurnOutputRequest{Outcome: contract.TurnCompleted})
	if r.handoff() != 0 {
		t.Fatalf("the next turn left the handoff: %d", r.handoff())
	}
}

// A switch onto agy is held to the account's limit in the same transaction.
func TestHarnessSwitchHoldsTheAgyLimit(t *testing.T) {
	r := newHandoffRig(t)
	if err := r.switchTo("g", &HarnessSwitch{Profiles: []contract.ProfileName{"p"}, Limit: 1}); err != ErrProfileFull {
		t.Fatalf("err = %v", err)
	}
	if r.handoff() != 0 {
		t.Fatal("a refused switch left a handoff")
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

// A turn records the profile it was dispatched under, so a switch while it runs does not change which harness served it.
func TestTurnKeepsTheProfileItWasDispatchedUnder(t *testing.T) {
	r := newHandoffRig(t)
	r.owner("work")
	r.start("t1")
	if err := r.switchTo("q", &HarnessSwitch{}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.s.TurnProfile(r.ctx, "s", "t1"); err != nil || got != "p" {
		t.Fatalf("turn profile %q: %v", got, err)
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
	if err := r.s.NoteBoxHello(r.ctx, "b", true); err != nil {
		t.Fatal(err)
	}
	if fence, err := r.s.HandoffFence(r.ctx, "s"); err != nil || fence != 0 {
		t.Fatalf("fence after the hello %d: %v", fence, err)
	}
	if got := r.start("t1"); got != "t1" {
		t.Fatalf("the restarted box got turn %q", got)
	}
}
