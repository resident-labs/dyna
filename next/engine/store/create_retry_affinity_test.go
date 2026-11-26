package store

import (
	"database/sql"
	"path/filepath"
	"strings"
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
	_, err := r.s.StartSession(r.ctx, Session{ID: "fresh", Room: "r", Name: "fresh", Profile: "p", Box: "b-fresh"}, "worker", contract.BoxSizeAgent, "", event, pending, "", true, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.writer.Exec("UPDATE boxes SET state='creating' WHERE id='b-fresh'"); err != nil {
		t.Fatal(err)
	}
	b, _ := r.s.Box(r.ctx, "b-fresh")
	r.finishAllocation(b, contract.CauseCreateFailed, "original visible failure", "")
	after, _ := r.s.Box(r.ctx, b.ID)
	if after.State != contract.BoxFailed || after.Host != "arch" || !strings.Contains(after.Reason, "original visible failure; automatic other-host retry skipped: initial pinned placement") {
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
			for _, query := range []string{"DROP INDEX deliveries_open; ALTER TABLE deliveries DROP COLUMN taken_at; ALTER TABLE turns DROP COLUMN model_sent_at; ALTER TABLE deliveries DROP COLUMN immediate",
				"DROP TABLE pr_stack_layers", "ALTER TABLE pr_owners DROP COLUMN stack", "ALTER TABLE pr_owners DROP COLUMN position", "ALTER TABLE hosts DROP COLUMN ci_scale", "ALTER TABLE boxes DROP COLUMN create_retry_host", "DROP INDEX goals_holder; CREATE INDEX goals_holder ON goals(session) WHERE state='active'; ALTER TABLE goal_items DROP COLUMN parent; ALTER TABLE goals DROP COLUMN folded_goal; ALTER TABLE goals DROP COLUMN folded_item; ALTER TABLE workflow_runs DROP COLUMN released_steps; ALTER TABLE rooms DROP COLUMN advisor_profile; DROP TABLE agents; ALTER TABLE rooms DROP COLUMN git_signing; ALTER TABLE workflow_runs DROP COLUMN paused_steps; ALTER TABLE workflow_runs DROP COLUMN paused; DROP INDEX workflow_runs_service; DROP TABLE workflow_streaks; ALTER TABLE workflow_runs DROP COLUMN served; ALTER TABLE workflow_runs DROP COLUMN sub; ALTER TABLE workflow_runs DROP COLUMN key; DROP TABLE workflow_queue; DROP TABLE workflow_state; DROP TABLE workflow_subscriptions; ALTER TABLE room_repos DROP COLUMN pr_events; ALTER TABLE room_repos DROP COLUMN pr_census_at; ALTER TABLE workflow_steps DROP COLUMN event; ALTER TABLE workflow_steps DROP COLUMN progress_offset; ALTER TABLE workflow_steps DROP COLUMN tool_calls; ALTER TABLE workflow_steps DROP COLUMN last_activity; ALTER TABLE room_repos DROP COLUMN ci_hosts; ALTER TABLE workflow_steps DROP COLUMN raw_text; DROP TABLE browsers; ALTER TABLE rooms DROP COLUMN browser; DROP TABLE service_feed_messages; DROP TABLE service_outbox; ALTER TABLE services DROP COLUMN feed_in; ALTER TABLE services DROP COLUMN feed_out; ALTER TABLE services DROP COLUMN feed_last_at; ALTER TABLE boxes DROP COLUMN can_handoff; ALTER TABLE boxes DROP COLUMN hello_seq; DROP TABLE agy_streak; ALTER TABLE sessions DROP COLUMN handoff; ALTER TABLE sessions DROP COLUMN agy_account; ALTER TABLE sessions DROP COLUMN cleared_at; ALTER TABLE turns DROP COLUMN cleared_at; UPDATE meta SET value='96' WHERE key='schema_version'"} {
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
			if scenario == "unbound" {
				want = contract.BoxPending
			}
			if result.State != want {
				t.Fatalf("legacy %s placement: %+v", scenario, result)
			}
			if want == contract.BoxFailed && (result.Host != "arch" || !strings.HasPrefix(result.Reason, "original visible failure; automatic other-host retry skipped:")) {
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
