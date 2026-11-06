package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func newCreateRetryRig(t *testing.T, initialHost *contract.HostName) (*failureRig, box.Box) {
	t.Helper()
	r := newFailureRig(t)
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('arch','','ready',8,34359738368,1099511627776,0),('spare','','ready',8,34359738368,1099511627776,0);
UPDATE boxes SET host='arch',state='creating',image='',failures=2 WHERE id='b-worker'`)
		if err != nil {
			return err
		}
		if initialHost != nil {
			_, err = r.s.AppendEvent(tx, contract.Event{ID: "initial-placement", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "b-worker", Session: "worker", Host: *initialHost, To: contract.BoxPending}})
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	b, err := r.s.Box(r.ctx, "b-worker")
	if err != nil {
		t.Fatal(err)
	}
	return r, b
}
func (r *failureRig) finishAllocation(b box.Box, cause contract.FailureCause, failure, image string) {
	r.t.Helper()
	r.events++
	event := contract.Event{ID: contract.EventID(fmt.Sprintf("allocation-%d", r.events)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Cause: cause, Phase: contract.CreatePhaseBox}}
	if err := r.s.FinishCreate(r.ctx, b, image, failure, r.now, 3, event, true); err != nil {
		r.t.Fatal(err)
	}
}
func TestCreateFailureDefersTerminalNoticeForOtherHostRetry(t *testing.T) {
	r, b := newCreateRetryRig(t, new(contract.HostName))
	r.finishAllocation(b, contract.CauseCreateFailed, "hung host", "")
	after, err := r.s.Box(r.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != contract.BoxPending || after.Host != "" {
		t.Fatalf("first create failure was terminal instead of a detached retry: %+v", after)
	}
	r.tick()
	if notices := r.notices("lead"); len(notices) != 0 {
		t.Fatalf("premature terminal notice: %v", notices)
	}
	var count int
	if err := r.s.Read().QueryRow("SELECT create_failures FROM hosts WHERE name='arch'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("host fault lost: %d %v", count, err)
	}
	var leftovers int
	if err := r.s.Read().QueryRow("SELECT count(*) FROM room_leftovers WHERE host='arch' AND box='b-worker'").Scan(&leftovers); err != nil || leftovers != 1 {
		t.Fatalf("old resources untracked: %d %v", leftovers, err)
	}
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET host='spare',state='creating',failures=0 WHERE id=?", b.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	alternate, _ := r.s.Box(r.ctx, b.ID)
	// Replay of the original host's result cannot terminate a different allocation.
	r.finishAllocation(b, contract.CauseCreateFailed, "late old result", "")
	after, _ = r.s.Box(r.ctx, b.ID)
	if after.State != contract.BoxCreating || after.Host != "spare" {
		t.Fatalf("old result changed new allocation: %+v", after)
	}
	r.finishAllocation(alternate, "", "", "new-image")
	r.tick()
	if notices := r.notices("lead"); len(notices) != 0 {
		t.Fatalf("success sent terminal notice: %v", notices)
	}
	after, _ = r.s.Box(r.ctx, b.ID)
	if after.State != contract.BoxStarting || after.Host != "spare" {
		t.Fatalf("alternate: %+v", after)
	}
}
func TestOtherHostRetryBudgetSurvivesRestartAndFinalNoticeIsOnce(t *testing.T) {
	r, b := newCreateRetryRig(t, new(contract.HostName))
	r.finishAllocation(b, contract.CauseCreateFailed, "hung host", "")
	dir := filepath.Dir(r.s.lock.Name())
	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.s = reopened
	t.Cleanup(func() { reopened.Close() })
	rows, err := r.s.LifecycleBoxes(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ID == b.ID {
			found = true
			if row.RetryHost != "arch" {
				t.Fatalf("retry budget reset on restart: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("retry disappeared")
	}
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET host='spare',state='creating' WHERE id=?", b.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	alternate, _ := r.s.Box(r.ctx, b.ID)
	r.finishAllocation(alternate, contract.CauseCreateFailed, "second host failed", "")
	after, _ := r.s.Box(r.ctx, b.ID)
	if after.State != contract.BoxFailed || after.Host != "spare" {
		t.Fatalf("second failure did not exhaust budget: %+v", after)
	}
	r.tick()
	r.tick()
	if notices := r.notices("lead"); len(notices) != 1 {
		t.Fatalf("final notice count: %v", notices)
	}
}
func TestOtherHostRetryDoesNotRelocateExistingWorkOrOtherCauses(t *testing.T) {
	for _, scenario := range []string{"image", "early hello", "turn", "other cause", "stop", "close"} {
		t.Run(scenario, func(t *testing.T) {
			r, b := newCreateRetryRig(t, new(contract.HostName))
			query := ""
			cause := contract.CauseCreateFailed
			switch scenario {
			case "image":
				query = "UPDATE boxes SET image='existing-image' WHERE id='b-worker'"
			case "early hello":
				query = "UPDATE boxes SET state='ready' WHERE id='b-worker'"
			case "turn":
				query = `INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('turn','worker',100000000,'running','p',0,0,0,0)`
			case "other cause":
				cause = contract.CauseStartFailed
			case "stop":
				query = "UPDATE boxes SET want='stop' WHERE id='b-worker'"
			case "close":
				query = "UPDATE boxes SET want='close' WHERE id='b-worker'"
			}
			if query != "" {
				if _, err := r.s.writer.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			r.finishAllocation(b, cause, "failed", "")
			after, _ := r.s.Box(r.ctx, b.ID)
			if after.Host != "arch" {
				t.Fatalf("unsafe/cancelled box moved: %+v", after)
			}
			var budget string
			if err := r.s.Read().QueryRow("SELECT create_retry_host FROM boxes WHERE id=?", b.ID).Scan(&budget); err != nil || budget != "" {
				t.Fatalf("unrelated/unsafe failure consumed retry: %q %v", budget, err)
			}
			if scenario == "stop" || scenario == "close" {
				if after.State != contract.BoxCreating {
					t.Fatalf("cancelled result won: %+v", after)
				}
			} else if after.State != contract.BoxFailed {
				t.Fatalf("failure hidden: %+v", after)
			}
			if scenario == "image" || scenario == "early hello" || scenario == "turn" {
				if !strings.Contains(after.Reason, "unsafe") {
					t.Fatalf("missing unsafe-relocation reason: %+v", after)
				}
			}
			if scenario == "image" && after.Image != "existing-image" {
				t.Fatalf("existing volume identity lost: %+v", after)
			}
		})
	}
}
