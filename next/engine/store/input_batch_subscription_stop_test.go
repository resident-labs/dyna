package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

// A subscription may discover an already-routed event after Stop cancelled
// its wake. Promotion records provenance but must not undo that cancellation.
func TestInputBatchSubscriptionPromotionPreservesCancelledWake(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`); err != nil {
		t.Fatal(err)
	}
	event := contract.Event{ID: "post", Room: "r", Time: time.Unix(1000, 0), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: "routed"}}
	if err = s.Write(ctx, func(tx *sql.Tx) error {
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		event.Seq = seq
		if err = s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}); err != nil {
			return err
		}
		// This is the same durable cancellation Stop applies to unread non-owner inputs.
		if _, err = tx.ExecContext(ctx, "UPDATE deliveries SET wake=0 WHERE to_id='s'"); err != nil {
			return err
		}
		return s.fire(ctx, tx, Subscription{Subscription: contract.Subscription{ID: "sub", Wake: "s"}}, event)
	}); err != nil {
		t.Fatal(err)
	}
	var wake, immediate bool
	if err = s.Read().QueryRowContext(ctx, "SELECT wake,immediate FROM deliveries WHERE to_id='s'").Scan(&wake, &immediate); err != nil || wake || !immediate {
		t.Fatalf("promotion wake=%v immediate=%v: %v", wake, immediate, err)
	}
	if turn, _, _, err := s.Dispatch(ctx, "s", "must-not-start", contract.Event{ID: "start", Room: "r", Time: event.Time}, false); err != nil || turn != "" {
		t.Fatalf("cancelled delivery restarted: %q %v", turn, err)
	}
}
