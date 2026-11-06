package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestSameHostStartUsesTypedPlacementAndKeepsVisibleFailure(t *testing.T) {
	r, _ := newCreateRetryRig(t, new(contract.HostName))
	event := contract.Event{ID: "same-host-start", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionStarted{Session: "fresh", Name: "fresh", Parent: "worker", Profile: "p"}}
	pending := event
	pending.ID = "same-host-pending"
	pending.Body = contract.BoxChanged{Box: "b-fresh", Session: "fresh", To: contract.BoxPending}
	_, err := r.s.StartSession(r.ctx, Session{ID: "fresh", Room: "r", Name: "fresh", Profile: "p", Box: "b-fresh"}, "worker", contract.BoxSizeAgent, "", event, pending, "", true, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.writer.Exec("UPDATE boxes SET state='creating' WHERE id='b-fresh'"); err != nil {
		t.Fatal(err)
	}
	b, _ := r.s.Box(r.ctx, "b-fresh")
	r.finishAllocation(b, contract.CauseCreateFailed, "original visible failure", "")
	after, _ := r.s.Box(r.ctx, b.ID)
	if after.State != contract.BoxFailed || after.Host != "arch" || after.Reason != "original visible failure" {
		t.Fatalf("sameHost failure changed or moved: %+v", after)
	}
}
func TestLegacyTypedPlacementControlsRetryWithoutAnAffinityMigration(t *testing.T) {
	for _, scenario := range []string{"pinned", "unbound", "missing", "owner restart"} {
		t.Run(scenario, func(t *testing.T) {
			host := contract.HostName("arch")
			initial := &host
			if scenario == "unbound" {
				initial = new(contract.HostName)
			}
			if scenario == "missing" {
				initial = nil
			}
			r, b := newCreateRetryRig(t, initial)
			if scenario == "owner restart" {
				if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
					_, err := r.s.AppendEvent(tx, contract.Event{ID: "owner-restart-placement", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.BoxChanged{Box: b.ID, Session: b.Session, Host: b.Host, From: contract.BoxFailed, To: contract.BoxPending}})
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			var before sql.NullString
			if err := r.s.Read().QueryRow("SELECT group_concat(hex(body)) FROM events").Scan(&before); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Dir(r.s.lock.Name())
			if err := r.s.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", (box.Layout{Data: dir}).Store())
			if err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{"ALTER TABLE boxes DROP COLUMN create_retry_host", "UPDATE meta SET value='96' WHERE key='schema_version'"} {
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			r.s = reopened
			t.Cleanup(func() { reopened.Close() })
			var after sql.NullString
			if err := r.s.Read().QueryRow("SELECT group_concat(hex(body)) FROM events").Scan(&after); err != nil || before != after {
				t.Fatalf("historical events changed: %v", err)
			}
			r.finishAllocation(b, contract.CauseCreateFailed, "original visible failure", "")
			result, _ := r.s.Box(r.ctx, b.ID)
			want := contract.BoxFailed
			if scenario == "unbound" || scenario == "owner restart" {
				want = contract.BoxPending
			}
			if result.State != want {
				t.Fatalf("legacy %s placement: %+v", scenario, result)
			}
			if want == contract.BoxFailed && (result.Host != "arch" || result.Reason != "original visible failure") {
				t.Fatalf("legacy failure changed: %+v", result)
			}
		})
	}
}
func TestEarlierReadinessStillPreventsRelocationAfterPlacementTimeChanges(t *testing.T) {
	r, b := newCreateRetryRig(t, new(contract.HostName))
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := r.s.AppendEvent(tx, contract.Event{ID: "earlier-hello", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: b.ID, Session: b.Session, From: contract.BoxCreating, To: contract.BoxReady}})
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE boxes SET created_at=? WHERE id=?", r.now.Add(time.Second).UnixMilli(), b.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.finishAllocation(b, contract.CauseCreateFailed, "late result", "")
	after, _ := r.s.Box(r.ctx, b.ID)
	if after.State != contract.BoxFailed || after.Host != "arch" {
		t.Fatalf("previously ready allocation moved: %+v", after)
	}
}
