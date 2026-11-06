package store

import (
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestRepeatedStartStopIsANoopPreservingRetryState(t *testing.T) {
	for _, state := range []contract.BoxState{contract.BoxPending, contract.BoxCreating, contract.BoxStarting, contract.BoxStopped} {
		t.Run(string(state), func(t *testing.T) {
			r, b := newCreateRetryRig(t, new(contract.HostName))
			due := r.now.Add(time.Minute).UnixMilli()
			if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE boxes SET state=?,want='run',due_at=?,failures=2,create_retry_host='first-host' WHERE id=?", state, due, b.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			id, accepted, err := r.s.RequestStartStop(r.ctx, b.Session, r.now)
			if err != nil || !accepted || id != b.ID {
				t.Fatalf("first Stop: %s %v %v", id, accepted, err)
			}
			var savedDue int64
			var failures int
			var want, retryHost string
			read := func() {
				t.Helper()
				if err := r.s.Read().QueryRow("SELECT due_at,failures,want,create_retry_host FROM boxes WHERE id=?", b.ID).Scan(&savedDue, &failures, &want, &retryHost); err != nil {
					t.Fatal(err)
				}
			}
			read()
			if savedDue != 0 || failures != 2 || want != "stop" || retryHost != "first-host" {
				t.Fatalf("first Stop lost retry state: due=%d failures=%d want=%s retry=%s", savedDue, failures, want, retryHost)
			}
			if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE boxes SET due_at=?,failures=3 WHERE id=?", due, b.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				id, accepted, err = r.s.RequestStartStop(r.ctx, b.Session, r.now)
				if err != nil || !accepted || id != b.ID {
					t.Fatalf("repeated Stop: %s %v %v", id, accepted, err)
				}
				read()
				if savedDue != due || failures != 3 || want != "stop" || retryHost != "first-host" {
					t.Fatalf("repeated Stop changed cleanup retry state: due=%d wanted=%d failures=%d want=%s retry=%s", savedDue, due, failures, want, retryHost)
				}
			}
		})
	}
}
