package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type agyRig struct {
	t   *testing.T
	s   *Store
	ctx context.Context
	now time.Time
	n   int
}

func newAgyRig(t *testing.T) *agyRig {
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
	return &agyRig{t: t, s: s, ctx: context.Background(), now: time.Unix(1000, 0)}
}

func (r *agyRig) owner(text string) {
	r.t.Helper()
	r.n++
	post := contract.Event{ID: contract.EventID("post" + string(rune('a'+r.n))), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: text}}
	to := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
	if _, err := r.s.Post(r.ctx, post, []contract.Delivery{{To: to, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *agyRig) start(turn contract.TurnID) {
	r.t.Helper()
	r.n++
	started := contract.Event{ID: contract.EventID("start" + string(turn)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	if got, _, _, err := r.s.Dispatch(r.ctx, "s", turn, started, false); err != nil || got != turn {
		r.t.Fatalf("dispatch %s: %q %v", turn, got, err)
	}
}

func (r *agyRig) end(turn contract.TurnID, out contract.TurnOutputRequest) {
	r.t.Helper()
	out.Turn = turn
	ended := contract.Event{ID: contract.EventID("end" + string(turn)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Body: contract.TurnEnded{Session: "s", Turn: turn, Outcome: out.Outcome, Cause: out.Cause}}
	if err := r.s.EndTurn(r.ctx, "s", out, ended); err != nil {
		r.t.Fatal(err)
	}
}

func (r *agyRig) limit(turn contract.TurnID, reset int64, moveTo string) {
	r.t.Helper()
	r.end(turn, contract.TurnOutputRequest{Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: reset, OneWake: true, MoveTo: moveTo})
}

func (r *agyRig) account() (string, int64, int64) {
	r.t.Helper()
	var account string
	var handoff, retryAt int64
	if err := r.s.Read().QueryRowContext(r.ctx, "SELECT agy_account,handoff,model_retry_at FROM sessions WHERE id='s'").Scan(&account, &handoff, &retryAt); err != nil {
		r.t.Fatal(err)
	}
	return account, handoff, retryAt
}

func (r *agyRig) streak() int {
	r.t.Helper()
	var n int
	if err := r.s.Read().QueryRowContext(r.ctx, "SELECT count(*) FROM agy_streak WHERE session='s'").Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// A limit with another account free moves the session there and wakes it at once, not at the reset; the account that returned the
// limit is recorded for the streak, and a completed turn on the new box carries the handoff and ends the streak.
func TestAgyLimitMovesTheSessionToAnotherAccount(t *testing.T) {
	r := newAgyRig(t)
	r.owner("work")
	r.start("t1")
	r.limit("t1", 3600, "b")
	account, handoff, retryAt := r.account()
	if account != "b" || handoff == 0 || retryAt != r.now.UnixMilli() {
		t.Fatalf("after the limit: account %q, handoff %d, retry at %d (now %d)", account, handoff, retryAt, r.now.UnixMilli())
	}
	var reset int64
	var own string
	if err := r.s.Read().QueryRowContext(r.ctx, "SELECT account,reset_at FROM agy_streak WHERE session='s'").Scan(&own, &reset); err != nil || own != "main" || reset != r.now.Add(time.Hour).UnixMilli() {
		t.Fatalf("streak: account %q reset %d: %v", own, reset, err)
	}
	if err := r.s.WakeModelRetries(r.ctx, r.now); err != nil {
		t.Fatal(err)
	}
	var wakes int
	if err := r.s.Read().QueryRowContext(r.ctx, "SELECT count(*) FROM deliveries WHERE to_id='s' AND state='pending' AND wake=1").Scan(&wakes); err != nil || wakes != 1 {
		t.Fatalf("the move did not wake the session now: %d %v", wakes, err)
	}
	session, err := r.s.Session(r.ctx, "s")
	if err != nil || session.AgyAccount != "b" || !session.Handoff {
		t.Fatalf("session: %+v %v", session, err)
	}
	r.now = r.now.Add(time.Second)
	r.start("t2")
	r.end("t2", contract.TurnOutputRequest{Outcome: contract.TurnCompleted})
	if _, handoff, _ := r.account(); handoff != 0 || r.streak() != 0 {
		t.Fatalf("after a completed turn on the new account: handoff %d, streak %d", handoff, r.streak())
	}
}

// With no other account free the limit is recorded and the session waits for its reset, as with one account. A limit that moved it
// and then returned on the new account records that one too, so the next move never goes back to the first.
func TestAgyLimitWithNoFreeAccountOnlyRecordsTheStreak(t *testing.T) {
	r := newAgyRig(t)
	r.owner("work")
	r.start("t1")
	r.limit("t1", 3600, "")
	account, handoff, retryAt := r.account()
	if account != "" || handoff != 0 || retryAt != r.now.Add(time.Hour).UnixMilli() || r.streak() != 1 {
		t.Fatalf("account %q, handoff %d, retry at %d, streak %d", account, handoff, retryAt, r.streak())
	}
	limited, err := r.s.AgyStreak(r.ctx, "s", r.now)
	if err != nil || !limited["main"] {
		t.Fatalf("streak: %v %v", limited, err)
	}
	// An entry expires at its reset.
	if limited, err := r.s.AgyStreak(r.ctx, "s", r.now.Add(time.Hour)); err != nil || len(limited) != 0 {
		t.Fatalf("an entry outlived its reset: %v %v", limited, err)
	}
	// New input from the owner ends the streak.
	r.owner("again")
	if r.streak() != 0 {
		t.Fatalf("an owner post left the streak: %d", r.streak())
	}
}

// A turn that was already running when a handoff became due has not carried it: only a turn that starts afterwards clears it.
func TestHandoffOutlastsTheTurnRunningWhenItWasSet(t *testing.T) {
	r := newAgyRig(t)
	r.owner("work")
	r.start("t1")
	event := contract.Event{ID: "switch", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.SessionProfileChanged{Session: "s", Profile: "q"}}
	if err := r.s.SetSessionProfile(r.ctx, "s", "q", event, &HarnessSwitch{}); err != nil {
		t.Fatal(err)
	}
	if account, handoff, _ := r.account(); account != "" || handoff == 0 {
		t.Fatalf("after the switch: account %q, handoff %d", account, handoff)
	}
	r.end("t1", contract.TurnOutputRequest{Outcome: contract.TurnCompleted})
	if _, handoff, _ := r.account(); handoff == 0 {
		t.Fatal("the turn that was running carried the handoff")
	}
	r.owner("next")
	r.now = r.now.Add(time.Second)
	r.start("t2")
	r.end("t2", contract.TurnOutputRequest{Outcome: contract.TurnCompleted})
	if _, handoff, _ := r.account(); handoff != 0 {
		t.Fatalf("the next turn left the handoff: %d", handoff)
	}
}

// A switch onto agy is held to the account's limit in the same transaction.
func TestHarnessSwitchHoldsTheAgyLimit(t *testing.T) {
	r := newAgyRig(t)
	event := contract.Event{ID: "switch", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.SessionProfileChanged{Session: "s", Profile: "g"}}
	if _, err := r.s.writer.Exec("UPDATE sessions SET profile='g' WHERE id='s'"); err != nil {
		t.Fatal(err)
	}
	err := r.s.SetSessionProfile(r.ctx, "s", "g", event, &HarnessSwitch{Profiles: []contract.ProfileName{"g"}, Limit: 1})
	if err != ErrProfileFull {
		t.Fatalf("err = %v", err)
	}
}
