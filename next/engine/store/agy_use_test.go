package store

import (
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// While a switch is held no agy session takes a turn or an idle compaction, and the retry wake starts nothing, by the same check as the
// usage limit; a pig session is untouched. Finishing releases them in the one transaction that makes the account active.
func TestHeldSwitchHoldsDispatchCompactionAndTheRetryWake(t *testing.T) {
	r := newLimitRig(t)
	for _, session := range []contract.SessionID{"b", "pig"} {
		r.post(session)
		turn := r.dispatch(session)
		ended := contract.Event{ID: contract.EventID("done-" + string(session)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalSession, Session: session}, Body: contract.TurnEnded{Session: session, Turn: turn, Outcome: contract.TurnCompleted}}
		if err := r.s.EndTurn(r.ctx, session, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted}, ended); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.s.BeginAgySwitch(r.ctx, "agy_new"); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Minute)
	if _, due, err := r.s.CompactionDue(r.ctx, "b", true, r.now); err != nil || due {
		t.Fatalf("b compacts while held: %v %v", due, err)
	}
	if _, due, err := r.s.CompactionDue(r.ctx, "pig", false, r.now); err != nil || !due {
		t.Fatalf("a pig session lost its compaction: %v %v", due, err)
	}
	r.input("b", owner, true)
	if r.dispatch("b") != "" {
		t.Fatal("b took a turn while held")
	}
	// The retry wake: a due retry time kicks the session, and the dispatch it asks for is still refused.
	if _, err := r.s.writer.Exec("UPDATE sessions SET model_retry_at=1 WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	r.sweep()
	if r.dispatch("b") != "" {
		t.Fatal("the retry wake started b while held")
	}
	r.kicked = nil
	if err := r.s.FinishAgySwitch(r.ctx, "agy_new"); err != nil {
		t.Fatal(err)
	}
	use, err := r.s.AgyUse(r.ctx)
	if err != nil || use.Active != "agy_new" || use.Holder != "agy_new" || use.Held || use.Target != "" || use.StuckHost != "" {
		t.Fatalf("after finish: %+v %v", use, err)
	}
	if !slices.Contains(r.kicked, "b") || slices.Contains(r.kicked, "pig") {
		t.Fatalf("release kicked %v", r.kicked)
	}
	if r.dispatch("b") == "" {
		t.Fatal("b did not start after the release")
	}
	if _, due, err := r.s.CompactionDue(r.ctx, "b", true, r.now); err != nil || !due {
		t.Fatalf("b does not compact after the release: %v %v", due, err)
	}
}

// The limit's key is the active account's id once there is one, "main" before; importing the signed-in account renames its row, and a
// switch makes the other account's row, if it has one, the one that applies.
func TestAgyLimitKeyFollowsTheActiveAccount(t *testing.T) {
	r := newLimitRig(t)
	r.limited(3600)
	if r.limit() == 0 {
		t.Fatal("no limit row under main")
	}
	if err := r.s.ActivateAgyAccount(r.ctx, "agy_second"); err != nil {
		t.Fatal(err)
	}
	if r.limit() != 0 || r.count("SELECT count(*) FROM agy_limits WHERE account='agy_second'") != 1 {
		t.Fatal("the main row was not renamed to the imported account")
	}
	if limited, err := r.s.agyLimited(r.ctx, r.s.Read(), r.now); err != nil || !limited {
		t.Fatalf("the active account's limit does not apply: %v %v", limited, err)
	}
	// Another account has no row: switching to it frees the sessions of the limit the first one is under.
	if err := r.s.FinishAgySwitch(r.ctx, "agy_first"); err != nil {
		t.Fatal(err)
	}
	if limited, err := r.s.agyLimited(r.ctx, r.s.Read(), r.now); err != nil || limited {
		t.Fatalf("the old account's limit still applies: %v %v", limited, err)
	}
	// A new limit lands on the new active account's row.
	r.limited(60)
	if r.count("SELECT count(*) FROM agy_limits WHERE account='agy_first'") != 1 {
		t.Fatal("a limit was not recorded under the active account")
	}
}

// A switch that changed nothing is aborted and releases; one left half-done (the engine host took the new token) stays held, because the
// engine host holds a token that is not the active account's.
func TestAbortReleasesAnUntouchedSwitchButNotAHalfDoneOne(t *testing.T) {
	r := newLimitRig(t)
	if err := r.s.ActivateAgyAccount(r.ctx, "agy_old"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.BeginAgySwitch(r.ctx, "agy_new"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.AbortAgySwitch(r.ctx); err != nil {
		t.Fatal(err)
	}
	if use, _ := r.s.AgyUse(r.ctx); use.Held || use.Active != "agy_old" || use.Target != "" {
		t.Fatalf("an untouched switch stayed held: %+v", use)
	}
	if err := r.s.BeginAgySwitch(r.ctx, "agy_new"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.NoteAgyHolder(r.ctx, "agy_new"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.StickAgySwitch(r.ctx, "node"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.AbortAgySwitch(r.ctx); err != nil {
		t.Fatal(err)
	}
	use, _ := r.s.AgyUse(r.ctx)
	if !use.Held || use.Target != "agy_new" || use.Holder != "agy_new" || use.Active != "agy_old" {
		t.Fatalf("a half-done switch was released: %+v", use)
	}
	for _, id := range []contract.AccountID{"agy_old", "agy_new"} {
		if inUse, err := r.s.AgyAccountInUse(r.ctx, id); err != nil || !inUse {
			t.Fatalf("%s is not in use: %v %v", id, inUse, err)
		}
	}
	if inUse, _ := r.s.AgyAccountInUse(r.ctx, "agy_other"); inUse {
		t.Fatal("an unrelated account is in use")
	}
}

func TestAgyRunningTurnsCountsOpenTurnsOnAgyBoxes(t *testing.T) {
	r := newLimitRig(t)
	if n, err := r.s.AgyRunningTurns(r.ctx); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	r.post("a")
	r.dispatch("a")
	r.post("pig")
	r.dispatch("pig")
	if n, err := r.s.AgyRunningTurns(r.ctx); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
}
