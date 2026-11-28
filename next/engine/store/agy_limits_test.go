package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// limitRig holds two agy sessions of one account (and a pig one) in a room, each with a ready box.
type limitRig struct {
	t      *testing.T
	ctx    context.Context
	dir    string
	s      *Store
	now    time.Time
	n      int
	kicked []contract.SessionID
}

func newLimitRig(t *testing.T) *limitRig {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := &limitRig{t: t, ctx: context.Background(), dir: dir, s: s, now: time.Unix(100000, 0)}
	s.SetWake(func(ids []contract.SessionID, _ []contract.Event) { r.kicked = append(r.kicked, ids...) })
	t.Cleanup(func() { s.Close() })
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('a','r','a','p','open','',0),('b','r','b','p','open','',0),('pig','r','pig','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,harness) VALUES('ba','a','','agent','ready','','',0,0,'agy'),('bb','b','','agent','ready','','',0,0,'agy'),('bp','pig','','agent','ready','','',0,0,'pig')`)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *limitRig) post(session contract.SessionID) {
	r.t.Helper()
	r.n++
	event := contract.Event{ID: contract.EventID(fmt.Sprintf("post%d", r.n)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "work"}}
	to := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	if _, err := r.s.Post(r.ctx, event, []contract.Delivery{{To: to, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
		r.t.Fatal(err)
	}
}

// dispatch tries to start a turn for the session and says which turn it got, "" for none.
func (r *limitRig) dispatch(session contract.SessionID) contract.TurnID {
	r.t.Helper()
	r.n++
	id := contract.TurnID(fmt.Sprintf("turn%d", r.n))
	started := contract.Event{ID: contract.EventID(fmt.Sprintf("start%d", r.n)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	turn, _, _, err := r.s.Dispatch(r.ctx, session, id, started, false)
	if err != nil {
		r.t.Fatal(err)
	}
	return turn
}

// fail ends the turn on a usage limit that resets after seconds (none if 0), as the engine hands it over for the serving harness.
func (r *limitRig) fail(session contract.SessionID, turn contract.TurnID, seconds int64, agy bool) {
	r.t.Helper()
	r.n++
	principal := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	ended := contract.Event{ID: contract.EventID(fmt.Sprintf("end%d", r.n)), Room: "r", Time: r.now, From: principal, Body: contract.TurnEnded{Session: session, Turn: turn, Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed}}
	output := contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: seconds, OneWake: agy}
	if err := r.s.EndTurn(r.ctx, session, output, ended); err != nil {
		r.t.Fatal(err)
	}
}

func (r *limitRig) count(query string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.s.Read().QueryRowContext(r.ctx, query, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *limitRig) retryAt(session contract.SessionID) int64 {
	r.t.Helper()
	var at int64
	if err := r.s.Read().QueryRowContext(r.ctx, "SELECT model_retry_at FROM sessions WHERE id=?", session).Scan(&at); err != nil {
		r.t.Fatal(err)
	}
	return at
}

func (r *limitRig) limit() int64 {
	r.t.Helper()
	var until int64
	err := r.s.Read().QueryRowContext(r.ctx, "SELECT until FROM agy_limits WHERE account='main'").Scan(&until)
	if err != nil {
		return 0
	}
	return until
}

func (r *limitRig) notices() []string {
	r.t.Helper()
	rows, err := r.s.Read().QueryContext(r.ctx, "SELECT json_extract(body,'$.text') FROM events WHERE type='notice.posted' ORDER BY seq")
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var all []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			r.t.Fatal(err)
		}
		all = append(all, text)
	}
	return all
}

// ownerPost and enginePost leave an input for the session, waking it, from the owner and from the engine.
func (r *limitRig) input(session contract.SessionID, from contract.Principal, wake bool) contract.DeliverySeq {
	r.t.Helper()
	r.n++
	event := contract.Event{ID: contract.EventID(fmt.Sprintf("input%d", r.n)), Room: "r", Time: r.now, From: from, Body: contract.PostCreated{Text: "work"}}
	to := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	seq, err := r.s.Post(r.ctx, event, []contract.Delivery{{To: to, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: wake}})
	if err != nil {
		r.t.Fatal(err)
	}
	return contract.DeliverySeq(seq)
}

var (
	owner  = contract.Principal{Kind: contract.PrincipalOwner}
	engine = contract.Principal{Kind: contract.PrincipalEngine}
)

// limited puts the account under a limit through session a, as a real turn that ended on it would.
func (r *limitRig) limited(seconds int64) {
	r.t.Helper()
	r.post("a")
	r.fail("a", r.dispatch("a"), seconds, true)
}

// sweep runs the sweep that wakes retries, at the rig's time, and says which sessions it queued a dispatch for.
func (r *limitRig) sweep() []contract.SessionID {
	r.t.Helper()
	r.kicked = nil
	if err := r.s.WakeModelRetries(r.ctx, r.now); err != nil {
		r.t.Fatal(err)
	}
	return r.kicked
}

func (r *limitRig) wakes(session contract.SessionID) (wake1, wake0 int) {
	r.t.Helper()
	return r.count("SELECT count(*) FROM deliveries WHERE to_id=? AND state='pending' AND wake=1", session), r.count("SELECT count(*) FROM deliveries WHERE to_id=? AND state='pending' AND wake=0", session)
}

// A usage limit on one session holds every session of the account: they take no turn and nothing of theirs changes, the owner hears once,
// and at the reset the sweep deletes the row, queues a dispatch for the agy sessions, and the held input starts.
func TestAgyLimitHoldsEverySessionOfTheAccountUntilTheReset(t *testing.T) {
	r := newLimitRig(t)
	r.limited(3600)
	until := r.now.Add(time.Hour).UnixMilli()
	if got := r.limit(); got != until {
		t.Fatalf("limit %d, want %d", got, until)
	}
	seq := r.input("b", owner, true)
	if got := r.dispatch("b"); got != "" {
		t.Fatalf("b took turn %q under the limit", got)
	}
	if got := r.retryAt("b"); got != 0 {
		t.Fatalf("the gate wrote b's retry time: %d", got)
	}
	if w1, w0 := r.wakes("b"); w1 != 1 || w0 != 0 {
		t.Fatalf("b's input changed: wake1 %d wake0 %d", w1, w0)
	}
	notices := r.notices()
	if len(notices) != 1 || !strings.Contains(notices[0], "Antigravity quota reached; neon sessions resume at 04:46 UTC") {
		t.Fatalf("notices %q", notices)
	}
	r.input("b", owner, true)
	if got := r.dispatch("b"); got != "" {
		t.Fatalf("b took turn %q for a second input", got)
	}
	if got := r.notices(); len(got) != 1 {
		t.Fatalf("notices repeated: %q", got)
	}
	r.now = r.now.Add(time.Hour - time.Second)
	if kicked := r.sweep(); len(kicked) != 0 || r.limit() == 0 {
		t.Fatalf("released before the reset: %v", kicked)
	}
	r.now = r.now.Add(time.Second)
	kicked := r.sweep()
	if r.limit() != 0 || !slices.Contains(kicked, "a") || !slices.Contains(kicked, "b") || slices.Contains(kicked, "pig") {
		t.Fatalf("release: row %d, kicked %v", r.limit(), kicked)
	}
	turn := r.dispatch("b")
	if turn == "" {
		t.Fatal("b did not start at the release")
	}
	var taken contract.DeliverySeq
	if err := r.s.Read().QueryRowContext(r.ctx, "SELECT MIN(seq) FROM deliveries WHERE to_id='b' AND turn=?", turn).Scan(&taken); err != nil || taken != seq {
		t.Fatalf("the turn took delivery %d, want the first input %d: %v", taken, seq, err)
	}
	if again := r.sweep(); len(again) != 0 {
		t.Fatalf("the release kicked twice: %v", again)
	}
}

// An owner's post keeps its wake through a Stop (which clears the retry time), so at the release, with no other event, the same post starts.
func TestAgyLimitOwnerPostThenStopStartsAtTheRelease(t *testing.T) {
	r := newLimitRig(t)
	r.limited(3600)
	seq := r.input("b", owner, true)
	if r.dispatch("b") != "" {
		t.Fatal("b started under the limit")
	}
	if _, _, err := r.s.RequestTurnStop(r.ctx, "b", r.now); err != nil {
		t.Fatal(err)
	}
	if w1, _ := r.wakes("b"); w1 != 1 {
		t.Fatalf("the stop cleared the owner's wake: %d", w1)
	}
	r.now = r.now.Add(time.Hour)
	if kicked := r.sweep(); !slices.Contains(kicked, "b") {
		t.Fatalf("b was not kicked: %v", kicked)
	}
	turn := r.dispatch("b")
	if turn == "" {
		t.Fatal("the owner's post did not start at the release")
	}
	var taken contract.DeliverySeq
	if err := r.s.Read().QueryRowContext(r.ctx, "SELECT seq FROM deliveries WHERE to_id='b' AND turn=?", turn).Scan(&taken); err != nil || taken != seq {
		t.Fatalf("the turn took delivery %d, want the owner's post %d: %v", taken, seq, err)
	}
}

// What a Stop cancelled stays cancelled across the release, and an input that never woke the session still does not.
func TestAgyLimitReleaseDoesNotRevive(t *testing.T) {
	r := newLimitRig(t)
	r.limited(3600)
	r.input("b", engine, true)
	if r.dispatch("b") != "" {
		t.Fatal("b started under the limit")
	}
	if _, _, err := r.s.RequestTurnStop(r.ctx, "b", r.now); err != nil {
		t.Fatal(err)
	}
	r.input("b", engine, false)
	r.now = r.now.Add(time.Hour)
	r.sweep()
	if w1, w0 := r.wakes("b"); w1 != 0 || w0 != 2 {
		t.Fatalf("after the release: wake1 %d wake0 %d", w1, w0)
	}
	if turn := r.dispatch("b"); turn != "" {
		t.Fatalf("b started on inputs that never woke it: %q", turn)
	}
}

// An idle agy session takes no compaction while the limit holds, and does again once it is released; a pig session is never affected.
func TestAgyLimitStopsIdleCompactionUntilTheRelease(t *testing.T) {
	r := newLimitRig(t)
	for _, session := range []contract.SessionID{"b", "pig"} {
		r.post(session)
		turn := r.dispatch(session)
		principal := contract.Principal{Kind: contract.PrincipalSession, Session: session}
		ended := contract.Event{ID: contract.EventID("done-" + string(session)), Room: "r", Time: r.now, From: principal, Body: contract.TurnEnded{Session: session, Turn: turn, Outcome: contract.TurnCompleted}}
		if err := r.s.EndTurn(r.ctx, session, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted}, ended); err != nil {
			t.Fatal(err)
		}
	}
	r.limited(3600)
	if _, due, err := r.s.CompactionDue(r.ctx, "b", true, r.now); err != nil || due {
		t.Fatalf("b compacts under the limit: %v %v", due, err)
	}
	if _, due, err := r.s.CompactionDue(r.ctx, "pig", false, r.now); err != nil || !due {
		t.Fatalf("a pig session lost its compaction: %v %v", due, err)
	}
	r.now = r.now.Add(time.Hour)
	r.sweep()
	if _, due, err := r.s.CompactionDue(r.ctx, "b", true, r.now); err != nil || !due {
		t.Fatalf("b does not compact after the release: %v %v", due, err)
	}
}

// A held session is not failed to the check-in: its inputs wait, which reads as pending work.
func TestAgyLimitHeldSessionIsPendingNotFailed(t *testing.T) {
	r := newLimitRig(t)
	r.limited(3600)
	r.post("b")
	r.dispatch("b")
	err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		sessions, _, err := r.s.readCheckinRoom(r.ctx, tx, "r")
		for _, s := range sessions {
			if s.ID == "b" && (!s.Pending || s.Failure != "") {
				r.t.Fatalf("b: pending %v, failure %q", s.Pending, s.Failure)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// With no stated reset there is no row, as before, and the next session starts.
func TestAgyLimitWithNoStatedResetWritesNothing(t *testing.T) {
	r := newLimitRig(t)
	r.limited(0)
	if r.limit() != 0 || len(r.notices()) != 0 {
		t.Fatalf("a row or a notice without a reset: %d %q", r.limit(), r.notices())
	}
	r.post("b")
	if r.dispatch("b") == "" {
		t.Fatal("b did not start")
	}
}

// A second limit that resets sooner keeps the later reset, and the owner is not told again while one stands.
func TestAgyLimitKeepsTheLaterReset(t *testing.T) {
	r := newLimitRig(t)
	r.post("a")
	r.post("b")
	a, b := r.dispatch("a"), r.dispatch("b")
	r.fail("a", a, 7200, true)
	r.fail("b", b, 60, true)
	if got, want := r.limit(), r.now.Add(2*time.Hour).UnixMilli(); got != want {
		t.Fatalf("limit %d, want %d", got, want)
	}
	if got := r.notices(); len(got) != 1 {
		t.Fatalf("notices %q", got)
	}
}

// A turn that is not served by agy writes no row, whatever it says of a reset.
func TestAgyLimitIsNotWrittenForAnotherHarness(t *testing.T) {
	r := newLimitRig(t)
	r.post("pig")
	r.fail("pig", r.dispatch("pig"), 3600, false)
	if r.limit() != 0 || len(r.notices()) != 0 {
		t.Fatalf("a row or a notice from a pig turn: %d %q", r.limit(), r.notices())
	}
}

// A row at its time holds nothing even before the sweep has released it, and a pig session is never held.
func TestAgyLimitPastItsTimeAndOtherHarnessesHoldNothing(t *testing.T) {
	r := newLimitRig(t)
	r.limited(60)
	r.post("pig")
	if r.dispatch("pig") == "" {
		t.Fatal("a pig session was held")
	}
	r.now = r.now.Add(time.Minute)
	r.post("b")
	if r.dispatch("b") == "" {
		t.Fatal("b was held past the reset")
	}
}

// The limit is in the store: an engine restart keeps it, and the sweep after the restart releases it.
func TestAgyLimitSurvivesARestartAndIsReleasedAfterIt(t *testing.T) {
	r := newLimitRig(t)
	r.limited(3600)
	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	r.s = s
	s.SetWake(func(ids []contract.SessionID, _ []contract.Event) { r.kicked = append(r.kicked, ids...) })
	t.Cleanup(func() { s.Close() })
	r.post("b")
	if got := r.dispatch("b"); got != "" {
		t.Fatalf("b took turn %q after a restart", got)
	}
	r.now = r.now.Add(time.Hour)
	if kicked := r.sweep(); !slices.Contains(kicked, "b") || r.limit() != 0 {
		t.Fatalf("the sweep after the restart: row %d, kicked %v", r.limit(), kicked)
	}
	if r.dispatch("b") == "" {
		t.Fatal("b did not start after the release")
	}
}

// The release starts no box itself: a stopped box with a waiting input starts through WakeStopped, which waits for a paused room's resume.
func TestAgyLimitReleaseLeavesAStoppedBoxInAPausedRoomStopped(t *testing.T) {
	r := newLimitRig(t)
	r.limited(3600)
	if _, err := r.s.writer.Exec("UPDATE boxes SET state='stopped',want='stop' WHERE session='b'; UPDATE rooms SET paused_at=1"); err != nil {
		t.Fatal(err)
	}
	r.input("b", owner, true)
	r.now = r.now.Add(time.Hour)
	r.sweep()
	if err := r.s.WakeStopped(r.ctx); err != nil {
		t.Fatal(err)
	}
	want := func(state string) {
		t.Helper()
		var got string
		if err := r.s.Read().QueryRowContext(r.ctx, "SELECT want FROM boxes WHERE session='b'").Scan(&got); err != nil || got != state {
			t.Fatalf("b's box wants %q, want %q: %v", got, state, err)
		}
	}
	want("stop")
	if _, err := r.s.writer.Exec("UPDATE rooms SET paused_at=NULL"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.WakeStopped(r.ctx); err != nil {
		t.Fatal(err)
	}
	want("run")
}
