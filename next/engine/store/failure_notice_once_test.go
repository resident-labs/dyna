package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

type failureRig struct {
	t      *testing.T
	ctx    context.Context
	s      *Store
	now    time.Time
	events int
}

// newFailureRig holds a root, a lead under it and a worker under the lead, all with ready boxes.
func newFailureRig(t *testing.T) *failureRig {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root','r','root',NULL,'p','open','',0),('lead','r','lead','root','p','open','',0),('worker','r','worker','lead','p','open','',0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b-root','root',NULL,'','agent','ready','','',0,0),('b-lead','lead',NULL,'','agent','ready','','',0,0),('b-worker','worker',NULL,'','agent','ready','','',0,0);`)
	if err != nil {
		t.Fatal(err)
	}
	return &failureRig{t: t, ctx: context.Background(), s: s, now: time.Unix(100000, 0)}
}

// setBoxes moves boxes to a state now, as the reconciler does when a host drops out or comes back.
func (r *failureRig) setBoxes(state contract.BoxState, reason string, sessions ...contract.SessionID) {
	r.t.Helper()
	for _, id := range sessions {
		if _, err := r.s.writer.Exec("UPDATE boxes SET state=?,reason=?,updated_at=? WHERE session=?", state, reason, r.now.UnixMilli(), id); err != nil {
			r.t.Fatal(err)
		}
	}
}

// tick runs the failure pass the engine runs on its timer.
func (r *failureRig) tick() {
	r.t.Helper()
	err := r.s.SendFailureNotices(r.ctx, r.now, func(id contract.RoomID, body contract.EventBody) (contract.Event, error) {
		r.events++
		return contract.Event{ID: contract.EventID(fmt.Sprintf("ev_%d", r.events)), Room: id, Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body}, nil
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

// notices returns each session.failed delivered to a session, as the session names it lists.
func (r *failureRig) notices(to contract.SessionID) [][]string {
	r.t.Helper()
	rows, err := r.s.Read().QueryContext(r.ctx, "SELECT e.body FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_kind='session' AND d.to_id=? AND e.type='session.failed' ORDER BY d.seq", to)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		var data []byte
		var body contract.SessionFailed
		if err := rows.Scan(&data); err != nil {
			r.t.Fatal(err)
		}
		if err := json.Unmarshal(data, &body); err != nil {
			r.t.Fatal(err)
		}
		var names []string
		for _, failure := range body.Failures {
			names = append(names, failure.Name)
		}
		out = append(out, names)
	}
	if err := rows.Err(); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r *failureRig) record(session contract.SessionID) string {
	r.t.Helper()
	var failure string
	err := r.s.Read().QueryRowContext(r.ctx, "SELECT failure FROM checkins WHERE session=?", session).Scan(&failure)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return failure
}

func (r *failureRig) advance(d time.Duration) { r.now = r.now.Add(d) }

// The live case: a host keeps dropping out and coming back, and every box on it goes lost and ready again each time. Root
// hears once, not once per drop.
func TestFlappingBoxesAreToldOnce(t *testing.T) {
	r := newFailureRig(t)
	r.setBoxes(contract.BoxLost, "host unreachable", "lead", "worker")
	r.advance(5 * time.Second)
	r.tick()
	if got := r.notices("root"); len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("first loss: %v", got)
	}
	for range 5 {
		r.advance(50 * time.Second)
		r.setBoxes(contract.BoxReady, "", "lead", "worker")
		for range 3 {
			r.advance(10 * time.Second)
			r.tick()
		}
		r.setBoxes(contract.BoxLost, "host unreachable", "lead", "worker")
		for range 3 {
			r.advance(10 * time.Second)
			r.tick()
		}
	}
	if got := r.notices("root"); len(got) != 1 {
		t.Fatalf("root heard %d notices for one failure: %v", len(got), got)
	}
	if got := r.notices("lead"); len(got) != 0 {
		t.Fatalf("lead heard of its worker: %v", got)
	}
}

// Coming back is not news either, even across many timer passes, and the record clears once the box has stayed up.
func TestRecoveryIsQuietAndSettles(t *testing.T) {
	r := newFailureRig(t)
	r.setBoxes(contract.BoxLost, "host unreachable", "worker")
	r.tick()
	if got := r.notices("lead"); len(got) != 1 {
		t.Fatalf("loss: %v", got)
	}
	r.advance(time.Minute)
	r.setBoxes(contract.BoxReady, "", "worker")
	for range 12 {
		r.advance(time.Minute)
		r.tick()
	}
	if got := len(r.notices("lead")) + len(r.notices("root")); got != 1 {
		t.Fatalf("a recovery sent notices: %d in all", got)
	}
	if got := r.record("worker"); got != "" {
		t.Fatalf("record after %s up: %q", room.FailureSettle, got)
	}
}

// A box that stayed up past room.FailureSettle and then fails again is a new failure, and its parent hears of it.
func TestNewFailureAfterRecoveryIsTold(t *testing.T) {
	r := newFailureRig(t)
	r.setBoxes(contract.BoxLost, "host unreachable", "worker")
	r.tick()
	r.advance(time.Minute)
	r.setBoxes(contract.BoxReady, "", "worker")
	r.advance(room.FailureSettle - time.Second)
	r.tick()
	if got := r.record("worker"); got != room.FailureBox {
		t.Fatalf("record before the box settled: %q", got)
	}
	r.advance(time.Second)
	r.tick()
	if got := r.record("worker"); got != "" {
		t.Fatalf("record after the box settled: %q", got)
	}
	r.advance(10 * time.Minute)
	r.setBoxes(contract.BoxFailed, "container missing", "worker")
	r.tick()
	r.tick()
	if got := r.notices("lead"); len(got) != 2 || got[1][0] != "worker" {
		t.Fatalf("lead notices %v, want the first failure and the new one", got)
	}
}
