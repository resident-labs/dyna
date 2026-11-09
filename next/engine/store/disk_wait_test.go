package store

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestDiskWaitNotice(t *testing.T) {
	r := newFailureRig(t)
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('arch','','ready',8,34359738368,0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	change := func(state contract.BoxState, reason string, n int) {
		t.Helper()
		if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE boxes SET state=?,reason=?,host='arch' WHERE id='b-worker'`, state, reason)
			if err != nil {
				return err
			}
			_, err = r.s.AppendEvent(tx, contract.Event{ID: contract.EventID(fmt.Sprintf("wait-%d", n)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "b-worker", Session: "worker", Host: "arch", To: state, Reason: reason}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(at time.Time, want int) {
		t.Helper()
		if err := r.s.NotifyDiskWaits(r.ctx, at); err != nil {
			t.Fatal(err)
		}
		var got int
		if err := r.s.Read().QueryRow(`SELECT count(*) FROM events WHERE id LIKE 'disk-wait-%'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("got %d notices, want %d", got, want)
		}
	}
	change(contract.BoxPending, "disk", 1)
	check(r.now.Add(4*time.Minute), 0)
	check(r.now.Add(5*time.Minute), 1)
	check(r.now.Add(time.Hour), 1)
	change(contract.BoxPending, "cpu", 2)
	change(contract.BoxPending, "disk", 3)
	check(r.now.Add(time.Hour), 1)
	change(contract.BoxStarting, "", 4)
	change(contract.BoxPending, "disk", 5)
	check(r.now.Add(5*time.Minute), 2)
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE sessions SET parent=NULL WHERE id='worker'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	change(contract.BoxStarting, "", 6)
	change(contract.BoxPending, "disk", 7)
	check(r.now.Add(time.Hour), 2)
	var deliveries int
	if err := r.s.Read().QueryRow(`SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.id LIKE 'disk-wait-%' AND d.to_id='lead' AND d.wake=1`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 2 {
		t.Fatalf("parent deliveries = %d", deliveries)
	}
}
