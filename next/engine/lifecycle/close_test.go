package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// closeBoxes is a host that records what it starts and removes, and whose removal fails with removeErr.
type closeBoxes struct {
	runningBoxes
	removeErr error
	mu        sync.Mutex
	starts    int
	removes   int
	removed   []contract.BoxID
}

func (b *closeBoxes) Start(context.Context, contract.BoxID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.starts++
	return nil
}

func (b *closeBoxes) Remove(_ context.Context, id contract.BoxID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removes++
	if b.removeErr != nil {
		return b.removeErr
	}
	b.removed = append(b.removed, id)
	return nil
}

// newClosingRig holds box b of session s, which had a turn and is closing, on host h, in state from, with no agent connected.
func newClosingRig(t *testing.T, from contract.BoxState, boxes BoxAdapter) (*store.Store, *Reconciler, *time.Time, *channelView) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Unix(100000, 0)
	at := now.UnixMilli()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at) VALUES('h','','ready',64,137438953472,1099511627776,%[1]d,%[1]d);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('root','r','root','p','open','',%[1]d);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at,repo) VALUES('s','r','s','root','p','closing','',%[1]d,'o/r');
INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t','s',%[1]d,%[1]d,'completed','m',0,0,0,0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,want) VALUES('b','s','h','image','agent','%[2]s','create_failed','',%[1]d,%[1]d,'close')`, at, from))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	events := 0
	event := func(room contract.RoomID, from contract.Principal, body contract.EventBody) (contract.Event, error) {
		events++
		return contract.Event{ID: contract.EventID(fmt.Sprintf("ev_%d", events)), Room: room, Time: now, From: from, Body: body}, nil
	}
	view := &channelView{connected: map[contract.SessionID]bool{}}
	return st, New(st, boxes, view, nil, event, func() time.Time { return now }), &now, view
}

// closingPass runs one pass and the steps it starts, and returns the box and its session's state.
func closingPass(t *testing.T, st *store.Store, r *Reconciler) (box.Box, string) {
	t.Helper()
	ctx := context.Background()
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	b, err := st.Box(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err := st.Read().QueryRowContext(ctx, "SELECT state FROM sessions WHERE id='s'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	return b, state
}

func closeNotice(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.Read().QueryContext(context.Background(), "SELECT body FROM events WHERE type='session.notice' AND client_id='close-diff:b'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var notices []string
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		notices = append(notices, body)
	}
	return notices
}

// A close never starts a box to save anything: a box that is not connected is removed as it is, and the parent is told it was
// not checked.
func TestCloseRemovesAnUnconnectedBoxWithoutStartingIt(t *testing.T) {
	for _, from := range []contract.BoxState{contract.BoxFailed, contract.BoxClosing, contract.BoxStopped} {
		t.Run(string(from), func(t *testing.T) {
			boxes := &closeBoxes{}
			st, r, _, _ := newClosingRig(t, from, boxes)
			if b, _ := closingPass(t, st, r); b.State != contract.BoxDeleted || len(boxes.removed) != 1 {
				t.Fatalf("box %s (%s), removed %v", b.State, b.Reason, boxes.removed)
			}
			if _, s := closingPass(t, st, r); s != "closed" {
				t.Fatalf("session %s", s)
			}
			notices := closeNotice(t, st)
			if boxes.starts != 0 || len(notices) != 1 || !strings.Contains(notices[0], "not checked, nothing saved") {
				t.Fatalf("started %d times, notices %q", boxes.starts, notices)
			}
		})
	}
}

// A connected box with turns gets diffWait to report its diff. Its report, or the wait ending, lets the removal go, and the
// notice goes once whichever comes first.
func TestCloseOfAConnectedBoxWaitsForItsDiffThenRemoves(t *testing.T) {
	boxes := &closeBoxes{}
	st, r, _, view := newClosingRig(t, contract.BoxReady, boxes)
	view.set("s", true)
	if b, _ := closingPass(t, st, r); b.State != contract.BoxClosing || boxes.removes != 0 {
		t.Fatalf("box %s, removes %d: it did not wait for the diff", b.State, boxes.removes)
	}
	if err := st.RequestRemoval(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	if b, _ := closingPass(t, st, r); b.State != contract.BoxDeleted {
		t.Fatalf("box %s after the report", b.State)
	}
}

func TestCloseOfAConnectedBoxThatNeverReportsRemovesItAfterTheWait(t *testing.T) {
	boxes := &closeBoxes{}
	st, r, now, view := newClosingRig(t, contract.BoxReady, boxes)
	view.set("s", true)
	closingPass(t, st, r)
	*now = now.Add(diffWait - time.Second)
	if b, _ := closingPass(t, st, r); b.State != contract.BoxClosing || boxes.removes != 0 {
		t.Fatalf("box %s, removes %d, before the wait ended", b.State, boxes.removes)
	}
	*now = now.Add(time.Second)
	if b, _ := closingPass(t, st, r); b.State != contract.BoxDeleted {
		t.Fatalf("box %s after the wait", b.State)
	}
	if _, s := closingPass(t, st, r); s != "closed" {
		t.Fatalf("session %s", s)
	}
	if notices := closeNotice(t, st); len(notices) != 1 || !strings.Contains(notices[0], "not checked") {
		t.Fatalf("notices %q", notices)
	}
}

// A removal that fails keeps the box and tries again every 30 seconds, whatever the box held.
func TestCloseRetriesAFailedRemoval(t *testing.T) {
	boxes := &closeBoxes{removeErr: errors.New("podman rm: busy")}
	st, r, now, _ := newClosingRig(t, contract.BoxStopped, boxes)
	b, s := closingPass(t, st, r)
	if b.State != contract.BoxClosing || b.Reason != "podman rm: busy" || s != "closing" {
		t.Fatalf("box %s (%s), session %s", b.State, b.Reason, s)
	}
	*now = now.Add(closeRetry)
	if closingPass(t, st, r); boxes.removes != 2 {
		t.Fatalf("removes %d", boxes.removes)
	}
}
