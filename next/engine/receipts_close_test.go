package engine

import (
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestReceiptAfterActualClose(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	live, p := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "live", Profile: "test", Goal: "goal", Brief: "brief"})
	if p != nil {
		t.Fatal(p)
	}
	var seqs []int64
	for _, state := range []string{"pending", "taken", "read"} {
		post, p := s.Post(ctx, owner, contract.PostRequest{Room: room, Home: feedHome, Text: state, Mentions: []string{"worker", "live"}})
		if p != nil {
			t.Fatal(p)
		}
		seqs = append(seqs, post.Seq)
		if err := s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE deliveries SET state=? WHERE seq=?`, state, post.Seq)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.CloseSession(ctx, worker, contract.Event{ID: "closed-worker", Room: room, Time: time.Now(), From: owner, Body: contract.SessionClosed{Session: worker}}); err != nil {
		t.Fatal(err)
	}
	for _, caller := range []contract.Principal{owner, boxCaller(live.Session)} {
		got, p := s.EventsList(ctx, caller, contract.EventsListRequest{Room: room, ReceiptSeqs: seqs})
		if p != nil {
			t.Fatalf("actual close receipt: %v", p)
		}
		for i, seq := range seqs {
			entries := got.Receipts[seq]
			if len(entries) != 2 {
				t.Fatalf("actual close recipients: %+v", entries)
			}
			for _, entry := range entries {
				if entry.Session == worker {
					want := "closed"
					if i == 2 {
						want = "read"
					}
					if entry.State != want || !entry.Closed || entry.BatchDeadline != nil {
						t.Fatalf("closed receipt: %+v", entry)
					}
				} else if entry.Session != live.Session || entry.Closed || entry.State != []string{"pending", "taken", "read"}[i] {
					t.Fatalf("live receipt: %+v", entry)
				}
			}
		}
	}
	// No retention change: pending/taken rows really were deleted on close.
	var unread int
	if err := s.store.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM deliveries WHERE to_id=? AND state IN ('pending','taken')`, worker).Scan(&unread); err != nil || unread != 0 {
		t.Fatalf("unread retained: %d %v", unread, err)
	}
}
