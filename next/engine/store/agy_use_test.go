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
	if fresh, err := r.s.BeginAgySwitch(r.ctx, "agy_new"); err != nil || !fresh {
		t.Fatalf("begin: %v %v", fresh, err)
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
	// The retry wake skips agy sessions while held: a due retry time wakes nothing, sets nothing running and keeps its timer.
	if _, err := r.s.writer.Exec("UPDATE sessions SET model_retry_at=1 WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	if kicked := r.sweep(); len(kicked) != 0 {
		t.Fatalf("the retry wake woke %v while held", kicked)
	}
	if r.retryAt("b") != 1 {
		t.Fatalf("the retry timer moved while held: %d", r.retryAt("b"))
	}
	if r.dispatch("b") != "" {
		t.Fatal("b took a turn while held")
	}
	r.kicked = nil
	if err := r.s.FinishAgySwitch(r.ctx, "agy_new", r.now); err != nil {
		t.Fatal(err)
	}
	use, err := r.s.AgyUse(r.ctx)
	if err != nil || use.Active != "agy_new" || use.Held || use.Target != "" || use.StuckHost != "" {
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
	if err := r.s.FinishAgySwitch(r.ctx, "agy_first", r.now); err != nil {
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

// Begin says whether it set the hold, and Abort is a plain release: the engine aborts only a hold its own call set, and one an earlier run
// left stays until a run for its target finishes it.
func TestBeginSaysWhetherItSetTheHoldAndAbortReleases(t *testing.T) {
	r := newLimitRig(t)
	if err := r.s.ActivateAgyAccount(r.ctx, "agy_old"); err != nil {
		t.Fatal(err)
	}
	if fresh, err := r.s.BeginAgySwitch(r.ctx, "agy_new"); err != nil || !fresh {
		t.Fatalf("first begin: %v %v", fresh, err)
	}
	if err := r.s.StickAgySwitch(r.ctx, "node"); err != nil {
		t.Fatal(err)
	}
	if fresh, err := r.s.BeginAgySwitch(r.ctx, "agy_new"); err != nil || fresh {
		t.Fatalf("a begin on a held switch: %v %v", fresh, err)
	}
	if use, _ := r.s.AgyUse(r.ctx); !use.Held || use.Target != "agy_new" || use.StuckHost != "" || use.Active != "agy_old" {
		t.Fatalf("held: %+v", use)
	}
	for _, id := range []contract.AccountID{"agy_old", "agy_new"} {
		if inUse, err := r.s.AgyAccountInUse(r.ctx, id); err != nil || !inUse {
			t.Fatalf("%s is not in use: %v %v", id, inUse, err)
		}
	}
	if inUse, _ := r.s.AgyAccountInUse(r.ctx, "agy_other"); inUse {
		t.Fatal("an unrelated account is in use")
	}
	if err := r.s.AbortAgySwitch(r.ctx); err != nil {
		t.Fatal(err)
	}
	if use, _ := r.s.AgyUse(r.ctx); use.Held || use.Active != "agy_old" || use.Target != "" || use.StuckHost != "" {
		t.Fatalf("after abort: %+v", use)
	}
	if inUse, _ := r.s.AgyAccountInUse(r.ctx, "agy_new"); inUse {
		t.Fatal("the target is in use after the abort")
	}
}

// A session whose failed turn waits for the old account's reset is due at the sweep right after the switch finishes, when the new account
// is free, and wakes under the sweep's usual rules: its failed input starts, a cancelled timer (-1) stays cancelled, a paused room waits.
func TestFinishingASwitchMakesRetriesOfTheOldAccountDue(t *testing.T) {
	r := newLimitRig(t)
	if err := r.s.ActivateAgyAccount(r.ctx, "agy_old"); err != nil {
		t.Fatal(err)
	}
	r.post("a")
	r.fail("a", r.dispatch("a"), 3600, true) // the old account's limit: one hour
	reset := r.now.Add(time.Hour).UnixMilli()
	if r.retryAt("a") != reset {
		t.Fatalf("the retry is at %d, want the old reset %d", r.retryAt("a"), reset)
	}
	if _, err := r.s.writer.Exec("UPDATE sessions SET model_retry_at=-1 WHERE id='b'"); err != nil { // the owner cancelled b's retry
		t.Fatal(err)
	}
	if _, err := r.s.BeginAgySwitch(r.ctx, "agy_new"); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Minute)
	if kicked := r.sweep(); len(kicked) != 0 {
		t.Fatalf("woke %v while held", kicked)
	}
	if err := r.s.FinishAgySwitch(r.ctx, "agy_new", r.now); err != nil {
		t.Fatal(err)
	}
	if r.retryAt("a") != r.now.UnixMilli() || r.retryAt("b") != -1 {
		t.Fatalf("a is due at %d (now %d), b at %d", r.retryAt("a"), r.now.UnixMilli(), r.retryAt("b"))
	}
	// A paused room is not woken, and the sweep right after the finish wakes a, with its failed input.
	if _, err := r.s.writer.Exec("UPDATE rooms SET paused_at=1"); err != nil {
		t.Fatal(err)
	}
	if kicked := r.sweep(); slices.Contains(kicked, "a") {
		t.Fatalf("a paused room woke %v", kicked)
	}
	if _, err := r.s.writer.Exec("UPDATE rooms SET paused_at=NULL"); err != nil {
		t.Fatal(err)
	}
	kicked := r.sweep()
	if !slices.Contains(kicked, "a") || slices.Contains(kicked, "b") {
		t.Fatalf("the sweep after the finish woke %v", kicked)
	}
	if wake1, _ := r.wakes("a"); wake1 != 1 {
		t.Fatalf("a's failed input was not promoted to a wake: %d", wake1)
	}
}

// If the new account is itself limited, nothing is pulled forward: the retries stay at their own times.
func TestFinishingASwitchToALimitedAccountLeavesRetriesAlone(t *testing.T) {
	r := newLimitRig(t)
	if err := r.s.ActivateAgyAccount(r.ctx, "agy_old"); err != nil {
		t.Fatal(err)
	}
	r.post("a")
	r.fail("a", r.dispatch("a"), 3600, true)
	reset := r.retryAt("a")
	if _, err := r.s.writer.Exec("INSERT INTO agy_limits(account,until) VALUES('agy_new',?)", r.now.Add(2*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.BeginAgySwitch(r.ctx, "agy_new"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.FinishAgySwitch(r.ctx, "agy_new", r.now); err != nil {
		t.Fatal(err)
	}
	if r.retryAt("a") != reset {
		t.Fatalf("the retry moved to %d from %d", r.retryAt("a"), reset)
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
