package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestTakenTimestamps(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0);`)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1000, 0)
	for i, id := range []contract.EventID{"new", "steer"} {
		at := start.Add(time.Duration(i) * time.Second)
		ev := contract.Event{ID: id, Room: "r", Time: at, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "input"}}
		err = s.Write(ctx, func(tx *sql.Tx) error {
			seq, e := s.AppendEvent(tx, ev)
			if e != nil {
				return e
			}
			return s.InsertDelivery(ctx, tx, seq, ev.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Surface: contract.SurfaceEngine, Wake: true, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}})
		})
		if err != nil {
			t.Fatal(err)
		}
		dispatched := at.Add(10 * time.Millisecond)
		turn, inputs, _, e := s.Dispatch(ctx, "s", contract.TurnID("turn_"+string(id)), contract.Event{ID: contract.EventID("dispatch_" + string(id)), Room: "r", Time: dispatched, From: contract.Principal{Kind: contract.PrincipalEngine}}, false)
		if e != nil || len(inputs) != 1 {
			t.Fatalf("dispatch: %s %d %v", turn, len(inputs), e)
		}
		if turn != "turn_new" {
			t.Fatalf("turn=%s", turn)
		}
		var taken int64
		if e := s.Read().QueryRow("SELECT taken_at FROM deliveries WHERE seq=?", inputs[0].Seq).Scan(&taken); e != nil {
			t.Fatal(e)
		}
		if taken != dispatched.UnixMilli() {
			t.Fatalf("taken=%d want %d", taken, dispatched.UnixMilli())
		}
		if i == 0 {
			if e := s.MarkTurnModelSent(ctx, "s", dispatched.Add(time.Millisecond)); e != nil {
				t.Fatal(e)
			}
		}
		_, _, _, e = s.Dispatch(ctx, "s", "unused", contract.Event{ID: "replay", Room: "r", Time: dispatched.Add(time.Second), From: contract.Principal{Kind: contract.PrincipalEngine}}, true)
		if e != nil {
			t.Fatal(e)
		}
		var replayTaken, model int64
		if e := s.Read().QueryRow("SELECT taken_at,model_sent_at FROM deliveries JOIN turns ON turns.id=deliveries.turn WHERE seq=?", inputs[0].Seq).Scan(&replayTaken, &model); e != nil {
			t.Fatal(e)
		}
		if replayTaken != taken {
			t.Fatalf("replay overwrote taken: %d -> %d", taken, replayTaken)
		}
		if i == 1 && model >= taken {
			t.Fatalf("steer should be after first request: model=%d taken=%d", model, taken)
		}
	}
}
