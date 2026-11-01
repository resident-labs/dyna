package lifecycle

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type retryTestView struct{}

func (retryTestView) OpenTerminal(contract.SessionID) bool { return true }
func (retryTestView) Connected(contract.SessionID) bool    { return true }
func (retryTestView) SessionClosed(contract.SessionID)     {}

func TestPassWakesModelRetryWithoutEvent(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
   INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
   INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	who := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
	post := contract.Event{ID: "post", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "work"}}
	if _, err := st.Post(ctx, post, []contract.Delivery{{To: who, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
		t.Fatal(err)
	}
	start := contract.Event{ID: "start", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	if turn, _, _, err := st.Dispatch(ctx, "s", "turn", start, false); err != nil || turn != "turn" {
		t.Fatalf("dispatch %s: %v", turn, err)
	}
	end := contract.Event{ID: "end", Room: "r", Time: now, From: who, Body: contract.TurnEnded{Session: "s", Turn: "turn", Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed}}
	if err := st.EndTurn(ctx, "s", contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed}, end); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := st.Read().QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&before); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	r := New(st, nil, retryTestView{}, nil, nil, func() time.Time { return now })
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	var wake bool
	var after int
	if err := st.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries WHERE to_id='s' AND state='pending'").Scan(&wake); err != nil {
		t.Fatal(err)
	}
	if err := st.Read().QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !wake || after != before {
		t.Fatalf("wake=%v events %d -> %d", wake, before, after)
	}
}
