package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestRestartCommitRefusesStaleFailedSnapshot(t *testing.T) {
	for _, change := range []string{"updated_at=updated_at+1", "image='new-image'", "host='new'", "reason='new failure'", "want='close'", "state='ready'", "unconfirmed-debt"} {
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
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','old','old-image','agent','failed','failure','',0,0);`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			old, err := st.Box(ctx, "b")
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Write(ctx, func(tx *sql.Tx) error {
				query := `UPDATE boxes SET ` + change + ` WHERE id='b'`
				if change == "unconfirmed-debt" {
					query = `INSERT INTO room_leftovers(host,room,box,slice) VALUES('new','r','b','')`
				}
				_, err := tx.Exec(query)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// A current failed snapshot may begin explicit cleanup with debt, but
			// the post-cleanup commit must refuse any still-unconfirmed record.
			if current, err := st.CanRestartSession(ctx, old); err != nil || current != (change == "unconfirmed-debt") {
				t.Fatalf("restart snapshot preflight: %v %v", current, err)
			}
			event := contract.Event{ID: "reset", Room: "r", Time: time.Unix(1000, 0), From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.BoxChanged{}}
			notice := contract.Event{ID: "notice", Room: "r", Time: event.Time, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionRestarted{Session: "s"}}
			if ok, err := st.RestartSession(ctx, old, event, notice); err != nil || ok {
				t.Fatalf("stale reset committed: %v %v", ok, err)
			}
			var events int
			if err := st.Read().QueryRow(`SELECT count(*) FROM events`).Scan(&events); err != nil || events != 0 {
				t.Fatalf("stale commit published: %d %v", events, err)
			}
		})
	}
}
