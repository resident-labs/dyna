package engine

import (
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestSubscriptionWorkflowResultDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, final, data string
		outcome           contract.TurnOutcome
		event, silent     bool
	}{
		{"empty", "", "", contract.TurnCompleted, true, true},
		{"null", "null", "null", contract.TurnCompleted, true, true},
		{"value", "hello", "", contract.TurnCompleted, true, false},
		{"false", "false", "false", contract.TurnCompleted, true, false},
		{"failure", "", "", contract.TurnFailed, true, false},
		{"manual", "", "", contract.TurnCompleted, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := setupWorkflow(t)
			started, crew := w.start(sessionCaller(w.root), workflowScript)
			turn := w.turn(crew)
			if tc.event {
				if err := w.store.Write(w.ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(w.ctx, "UPDATE workflow_runs SET sub='sub_test' WHERE id=?", started.Run)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			output := contract.TurnOutputRequest{Turn: turn.Turn, Outcome: tc.outcome, Final: tc.final, Data: contract.JSONData(tc.data)}
			if tc.outcome == contract.TurnFailed {
				output.Error, output.Cause = "failed", contract.CauseWorkflowFailed
			}
			if _, p := w.TurnOutput(w.ctx, crew, output); p != nil {
				t.Fatal(p)
			}
			var n int
			if err := w.store.Read().QueryRowContext(w.ctx, `SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id=? AND json_extract(e.body,'$.session')=? AND e.type='turn.ended'`, w.root, crew.Session).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if (n == 0) != tc.silent {
				t.Fatalf("deliveries %d, silent %v", n, tc.silent)
			}
		})
	}
}
