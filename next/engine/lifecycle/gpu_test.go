package lifecycle

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// cardBoxes records the card each created container is given.
type cardBoxes struct {
	runningBoxes
	mu    sync.Mutex
	cards map[contract.BoxID]*int
}

func (c *cardBoxes) Create(_ context.Context, spec BoxSpec) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cards[spec.Box] = spec.GPUCard
	return "image", nil
}

func TestPassPlacesGPUBoxesOnCardsAndReleaseFreesVRAM(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Unix(100000, 0)
	at := now.UnixMilli()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at) VALUES('h','','ready',64,137438953472,1099511627776,%[1]d,%[1]d);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s1','r','s1','p','open','',%[1]d+1),('s2','r','s2','p','open','',%[1]d+2),('s3','r','s3','p','open','',%[1]d+3),('s4','r','s4','p','open','',%[1]d+4);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,gpu_gib) VALUES('b1','s1',NULL,'','agent','pending','','',%[1]d,%[1]d,8),('b2','s2',NULL,'','agent','pending','','',%[1]d,%[1]d,8),('b3','s3',NULL,'','agent','pending','','',%[1]d,%[1]d,14),('b4','s4',NULL,'','agent','pending','','',%[1]d,%[1]d,0)`, at))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Card 0 gives 9.9 GiB to boxes, card 1 gives 21.6.
	if err := st.SetHostGPUs(ctx, "h", []contract.GPU{{Index: 0, Name: "small", MemBytes: 11 * box.GiB}, {Index: 1, Name: "big", MemBytes: 24 * box.GiB}}); err != nil {
		t.Fatal(err)
	}
	boxes := &cardBoxes{cards: map[contract.BoxID]*int{}}
	events := 0
	event := func(room contract.RoomID, from contract.Principal, body contract.EventBody) (contract.Event, error) {
		events++
		return contract.Event{ID: contract.EventID(fmt.Sprintf("ev_%d", events)), Room: room, Time: now, From: from, Body: body}, nil
	}
	view := &channelView{connected: map[contract.SessionID]bool{}}
	r := New(st, boxes, view, nil, event, func() time.Time { return now })
	pass := func() map[contract.BoxID]store.LifecycleBox {
		t.Helper()
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		r.Wait()
		rows, err := st.LifecycleBoxes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[contract.BoxID]store.LifecycleBox{}
		for _, row := range rows {
			byID[row.ID] = row
		}
		return byID
	}
	rows := pass()
	for id, card := range map[contract.BoxID]int{"b1": 0, "b2": 1} {
		if rows[id].Host != "h" || rows[id].GPUCard != card || boxes.cards[id] == nil || *boxes.cards[id] != card {
			t.Errorf("%s: host %q card %d, container card %v; want card %d", id, rows[id].Host, rows[id].GPUCard, boxes.cards[id], card)
		}
	}
	if b3 := rows["b3"]; b3.State != contract.BoxPending || b3.Reason != string(box.PendingGPU) || b3.Host != "" {
		t.Errorf("b3 fits no card beside the others: %s %q on %q", b3.State, b3.Reason, b3.Host)
	}
	if b4 := rows["b4"]; b4.Host != "h" || boxes.cards["b4"] != nil {
		t.Errorf("a box with no GPU request: host %q container card %v", b4.Host, boxes.cards["b4"])
	}
	// b2 goes away, which frees its 8 GiB on card 1.
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='deleted',want='close' WHERE id='b2'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows = pass()
	if b3 := rows["b3"]; b3.Host != "h" || b3.GPUCard != 1 || boxes.cards["b3"] == nil || *boxes.cards["b3"] != 1 {
		t.Errorf("b3 after the release: host %q card %d, container card %v", b3.Host, b3.GPUCard, boxes.cards["b3"])
	}
}

func TestAGPUBoxWaitsForAHostNotForANode(t *testing.T) {
	row := store.LifecycleBox{Box: box.Box{ID: "b", GPU: 4}, Only: []string{"pool"}}
	pools := []store.Pool{{Name: "pool"}}
	if got := poolWait(row, box.PendingGPU, pools); got != box.PendingGPU {
		t.Fatalf("a GPU box waits for %q", got)
	}
	row.GPU = 0
	if got := poolWait(row, box.PendingGPU, pools); got != box.PendingPoolNode {
		t.Fatalf("a box with no GPU waits for %q", got)
	}
}
