package engine

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestReceiptHistoricalRecipients(t *testing.T) {
	for _, closed := range []string{"own", "foreign", "both"} {
		t.Run(closed, func(t *testing.T) {
			s, room, worker, ctx := setup(t)
			owner := contract.Principal{Kind: contract.PrincipalOwner}
			other, p := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
			if p != nil {
				t.Fatal(p)
			}
			// Historical identities need no box. A missing identity is not attributable
			// to any room and must be omitted, not resurrected from delivery data.
			err := s.store.Write(ctx, func(tx *sql.Tx) error {
				for _, pair := range []struct {
					id   string
					room contract.RoomID
				}{{"own", room}, {"foreign", other.Room}, {"deleted", room}, {"missing", room}} {
					if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES(?,?,?,'test','open','',0)`, pair.id, pair.room, pair.id); err != nil {
						return err
					}
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) SELECT id,id,'','agent','ready','','',0,0 FROM sessions WHERE id IN ('own','foreign','deleted','missing')`)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			var seqs []int64
			for i, state := range []string{"pending", "taken", "read"} {
				var ds []contract.Delivery
				for _, id := range []contract.SessionID{worker, "own", "foreign", "missing", "deleted"} {
					ds = append(ds, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: id}, Wake: true})
				}
				seq, err := s.store.Post(ctx, contract.Event{ID: contract.EventID(fmt.Sprintf("history-%d", i)), Room: room, Time: time.Now(), From: owner, Body: contract.PostCreated{Home: feedHome, Text: state}}, ds)
				if err != nil {
					t.Fatal(err)
				}
				seqs = append(seqs, seq)
				if err := s.store.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `UPDATE deliveries SET state=? WHERE seq=?`, state, seq)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `DELETE FROM boxes WHERE session IN ('own','foreign','deleted','missing')`); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id IN ('deleted','missing')`); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `UPDATE sessions SET state='closed',closed_at=1000 WHERE id=? OR (?='both' AND id IN ('own','foreign'))`, closed, closed)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for _, caller := range []contract.Principal{owner, boxCaller(worker)} {
				got, p := s.EventsList(ctx, caller, contract.EventsListRequest{Room: room, ReceiptSeqs: append(seqs, -1, 0, 99999999)})
				if p != nil {
					t.Fatalf("historical batch: %v", p)
				}
				if len(got.Receipts) != 3 {
					t.Fatalf("batch: %+v", got)
				}
				for i, seq := range seqs {
					want := 3
					if caller.Kind == contract.PrincipalSession {
						want = 2
					}
					entries := got.Receipts[seq]
					if len(entries) != want {
						t.Fatalf("recipients: %+v", entries)
					}
					for _, entry := range entries {
						if entry.Session == "missing" || entry.Session == "deleted" || (caller.Kind == contract.PrincipalSession && entry.Session == "foreign") {
							t.Fatalf("identity leaked: %+v", entry)
						}
						if entry.State != []string{"pending", "taken", "read"}[i] || entry.BatchDeadline != nil {
							t.Fatalf("historical truth: %+v", entry)
						}
					}
				}
			}
			if _, p := s.EventsList(ctx, owner, contract.EventsListRequest{Room: room, ReceiptSeqs: make([]int64, 101)}); p == nil || p.Code != contract.ErrInvalid {
				t.Fatal("unbounded receipt lookup")
			}
		})
	}
}
