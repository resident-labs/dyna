package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestPendingRestartCommitFencesHistoryAndSnapshot(t *testing.T) {
	for _, change := range []string{"none", "image='volume'", "updated_at=updated_at+1", "host='new'", "want='close'", "state='creating'", "debt", "history", "unknown"} {
		t.Run(change, func(t *testing.T) {
			st, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			ctx := context.Background()
			if err := st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('old','','ready',1,1,1,0),('new','','ready',1,1,1,0);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','old','','agent','pending','memory','',0,0);`)
				if err != nil {
					return err
				}
				_, err = st.AppendEvent(tx, contract.Event{ID: "initial", Room: "r", Time: time.UnixMilli(1), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "b", Session: "s", Host: "old", To: contract.BoxPending}})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			old, err := st.Box(ctx, "b")
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := st.CanRestartSession(ctx, old); err != nil || !ok {
				t.Fatalf("preflight: %v %v", ok, err)
			}
			if err := st.Write(ctx, func(tx *sql.Tx) error {
				query := `UPDATE boxes SET ` + change + ` WHERE id='b'`
				switch change {
				case "none":
					return nil
				case "debt":
					query = `INSERT INTO room_leftovers(host,room,box,slice) VALUES('old','r','b','')`
				case "unknown":
					query = `DELETE FROM events`
				case "history":
					_, err := st.AppendEvent(tx, contract.Event{ID: "create", Room: "r", Time: time.UnixMilli(2), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "b", Session: "s", Host: "old", From: contract.BoxPending, To: contract.BoxCreating}})
					return err
				}
				_, err := tx.Exec(query)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			event := contract.Event{ID: "reset", Room: "r", Time: time.UnixMilli(3), From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.BoxChanged{}}
			notice := contract.Event{ID: "notice", Room: "r", Time: event.Time, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionRestarted{Session: "s"}}
			ok, err := st.RestartSession(ctx, old, event, notice, "new")
			if err != nil || ok != (change == "none") {
				t.Fatalf("commit: %v %v", ok, err)
			}
			if ok {
				current, err := st.Box(ctx, "b")
				if err != nil || current.Host != "new" || current.Reason != "" {
					t.Fatalf("reset: %+v %v", current, err)
				}
				if err := st.Write(ctx, func(tx *sql.Tx) error {
					eligibility, err := allocationRetryEligibility(ctx, tx, "b", "r")
					if err == nil && eligibility != createRetryPinned {
						t.Fatalf("replacement pin lost: %v", eligibility)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
