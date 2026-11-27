package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// limitRig holds two agy sessions of one account (and a pig one) in a room, each with a ready box.
type limitRig struct {
	t   *testing.T
	ctx context.Context
	dir string
	s   *Store
	now time.Time
	n   int
}

func newLimitRig(t *testing.T) *limitRig {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('a','r','a','p','open','',0),('b','r','b','p','open','',0),('pig','r','pig','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,harness) VALUES('ba','a','','agent','ready','','',0,0,'agy'),('bb','b','','agent','ready','','',0,0,'agy'),('bp','pig','','agent','ready','','',0,0,'pig')`)
	if err != nil {
		t.Fatal(err)
	}
	return &limitRig{t: t, ctx: context.Background(), dir: dir, s: s, now: time.Unix(100000, 0)}
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

// A usage limit on one session holds every session of the account: they take no turn, keep their inputs, wake at the reset, and the owner
// hears once. At the reset the sweep wakes them and they start.
func TestAgyLimitHoldsEverySessionOfTheAccountUntilTheReset(t *testing.T) {
	r := newLimitRig(t)
	r.post("a")
	turn := r.dispatch("a")
	if turn == "" {
		t.Fatal("a did not start")
	}
	r.fail("a", turn, 3600, true)
	until := r.now.Add(time.Hour).UnixMilli()
	if got := r.limit(); got != until {
		t.Fatalf("limit %d, want %d", got, until)
	}
	r.post("b")
	if got := r.dispatch("b"); got != "" {
		t.Fatalf("b took turn %q under the limit", got)
	}
	if got := r.retryAt("b"); got != until {
		t.Fatalf("b retries at %d, want %d", got, until)
	}
	if n := r.count("SELECT count(*) FROM deliveries WHERE to_id='b' AND state='pending'"); n != 1 {
		t.Fatalf("b's input did not stay pending: %d", n)
	}
	notices := r.notices()
	if len(notices) != 1 || !strings.Contains(notices[0], "Antigravity quota reached; neon sessions resume at 04:46 UTC") {
		t.Fatalf("notices %q", notices)
	}
	// Another input, an owner's among them, is held the same way and adds no notice and no Google call.
	r.post("b")
	if got := r.dispatch("b"); got != "" {
		t.Fatalf("b took turn %q for a second input", got)
	}
	if got := r.notices(); len(got) != 1 {
		t.Fatalf("notices repeated: %q", got)
	}
	// Before the reset nothing wakes; at it both do and both start.
	r.now = r.now.Add(time.Hour - time.Second)
	if err := r.s.WakeModelRetries(r.ctx, r.now); err != nil {
		t.Fatal(err)
	}
	if n := r.count("SELECT count(*) FROM deliveries WHERE to_id IN ('a','b') AND state='pending' AND wake=1"); n != 0 {
		t.Fatalf("woken before the reset: %d", n)
	}
	r.now = r.now.Add(time.Second)
	if err := r.s.WakeModelRetries(r.ctx, r.now); err != nil {
		t.Fatal(err)
	}
	for _, session := range []contract.SessionID{"a", "b"} {
		if got := r.dispatch(session); got == "" {
			t.Fatalf("%s did not start at the reset", session)
		}
	}
}

// A held session is retrying, not failed, to the check-in that tells a parent of failures.
func TestAgyLimitHeldSessionIsRetryingNotFailed(t *testing.T) {
	r := newLimitRig(t)
	r.post("a")
	r.fail("a", r.dispatch("a"), 3600, true)
	r.post("b")
	r.dispatch("b")
	err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		sessions, _, err := r.s.readCheckinRoom(r.ctx, tx, "r")
		for _, s := range sessions {
			if s.ID == "b" && (!s.Retrying || s.Failure != "") {
				r.t.Fatalf("b: retrying %v, failure %q", s.Retrying, s.Failure)
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
	r.post("a")
	r.fail("a", r.dispatch("a"), 0, true)
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

// A row at its time counts for nothing, and a pig session is never held by one.
func TestAgyLimitPastItsTimeAndOtherHarnessesHoldNothing(t *testing.T) {
	r := newLimitRig(t)
	r.post("a")
	r.fail("a", r.dispatch("a"), 60, true)
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

// The limit is in the store: an engine restart keeps it.
func TestAgyLimitSurvivesARestart(t *testing.T) {
	r := newLimitRig(t)
	r.post("a")
	r.fail("a", r.dispatch("a"), 3600, true)
	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	r.s = s
	t.Cleanup(func() { s.Close() })
	r.post("b")
	if got := r.dispatch("b"); got != "" {
		t.Fatalf("b took turn %q after a restart", got)
	}
}
