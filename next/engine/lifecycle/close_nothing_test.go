package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// emptyHostBoxes is a host where a closing box's start fails with startErr, and which records what it removes.
type emptyHostBoxes struct {
	runningBoxes
	startErr error
	mu       sync.Mutex
	starts   int
	removed  []contract.BoxID
}

func (b *emptyHostBoxes) Start(context.Context, contract.BoxID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.starts++
	return b.startErr
}

func (b *emptyHostBoxes) Remove(_ context.Context, id contract.BoxID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removed = append(b.removed, id)
	return nil
}

// newClosingRig holds box b of session s, which had a turn and is closing, on host h, in state from, with no agent connected.
func newClosingRig(t *testing.T, from contract.BoxState, boxes BoxAdapter) (*store.Store, *Reconciler, *time.Time) {
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
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','closing','',%[1]d);
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
	return st, New(st, boxes, view, nil, event, func() time.Time { return now }), &now
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

// As on lt-24-005: a box whose create never worked has no container and no volume, so nothing to archive. Its close finishes
// instead of starting it every 10 minutes for ever.
func TestCloseOfABoxWithNothingOnItsHostFinishes(t *testing.T) {
	for _, from := range []contract.BoxState{contract.BoxFailed, contract.BoxClosing} {
		t.Run(string(from), func(t *testing.T) {
			boxes := &emptyHostBoxes{startErr: box.NothingOnHost{Box: "b", Host: "h"}}
			st, r, _ := newClosingRig(t, from, boxes)
			b, _ := closingPass(t, st, r)
			if b.State != contract.BoxDeleted || len(boxes.removed) != 1 {
				t.Fatalf("box %s (%s), removed %v", b.State, b.Reason, boxes.removed)
			}
			if _, s := closingPass(t, st, r); s != "closed" {
				t.Fatalf("session %s", s)
			}
			if boxes.starts != 1 {
				t.Fatalf("started %d times", boxes.starts)
			}
		})
	}
}

// A start that fails for any other reason keeps the box, its volume and the retries, and the reason says why.
func TestCloseOfABoxThatFailsToStartRetries(t *testing.T) {
	boxes := &emptyHostBoxes{startErr: errors.New("host command exited with status 125: Error: statfs /x: no such file or directory")}
	st, r, now := newClosingRig(t, contract.BoxFailed, boxes)
	b, s := closingPass(t, st, r)
	if b.State != contract.BoxClosing || b.Reason != boxes.startErr.Error() || len(boxes.removed) != 0 || s != "closing" {
		t.Fatalf("box %s (%s), removed %v, session %s", b.State, b.Reason, boxes.removed, s)
	}
	*now = now.Add(archiveRetry)
	if b, _ := closingPass(t, st, r); b.State != contract.BoxClosing || boxes.starts != 2 || len(boxes.removed) != 0 {
		t.Fatalf("box %s, starts %d, removed %v", b.State, boxes.starts, boxes.removed)
	}
}
