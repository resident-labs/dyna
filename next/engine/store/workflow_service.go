package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// maxHandOvers is how many times a row is handed to a box. A row still unacknowledged after the last one fails, so an event
// that takes its box down cannot loop.
const maxHandOvers = 3

// Served is what a service's run keeps of the events it has finished. The journal holds only the steps of events in a
// handler, so the usage of the rest lives here. Since is when the box first asked for events.
type Served struct {
	Since     time.Time      `json:"since,omitzero"`
	Handled   int            `json:"handled"`
	Failed    int            `json:"failed"`
	LastEvent time.Time      `json:"lastEvent,omitzero"`
	Usage     contract.Usage `json:"usage"`
}

// DroppedEvent is a row that failed without a handler's say: its box took it three times and never finished it.
type DroppedEvent struct {
	Row        int64
	Key        string
	FirstOfKey bool // no earlier failure of this key is waiting for a success to end it
	HandOvers  int
}

func updateServed(ctx context.Context, tx *sql.Tx, run contract.WorkflowRunID, change func(*Served)) error {
	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT served FROM workflow_runs WHERE id=?", run).Scan(&raw); err != nil {
		return err
	}
	var served Served
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &served); err != nil {
			return err
		}
	}
	change(&served)
	data, err := json.Marshal(served)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE workflow_runs SET served=? WHERE id=?", string(data), run)
	return err
}

// HandOverEvents claims for a service's box the rows it should handle now: the oldest row of each key that has none in a
// handler of this box start, at most limit. A row is in a handler when this box start claimed it and the box says it holds
// it. One that an earlier box start or run was handling, or whose reply to the box was lost, is handed over again. A row
// handed over maxHandOvers times already fails instead. The first call of a box also ends the run's backoff,
// because the script reached serve.
func (s *Store) HandOverEvents(ctx context.Context, run WorkflowRun, boot string, held []int64, limit int, now time.Time) ([]contract.WorkflowEvent, []DroppedEvent, error) {
	var events []contract.WorkflowEvent
	var dropped []DroppedEvent
	err := s.Write(ctx, func(tx *sql.Tx) error {
		events, dropped = nil, nil
		if run.Served.Since.IsZero() {
			if err := updateServed(ctx, tx, run.ID, func(served *Served) { served.Since = now }); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE subscriptions SET failures=0,hold_until=0 WHERE id=?", run.Subscription); err != nil {
				return err
			}
		}
		heldRows, err := json.Marshal(append([]int64{}, held...)) // [] and not null, which json_each reads as one NULL
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT q.id,q.key,q.body,q.handovers FROM workflow_queue q
			WHERE q.sub=?1 AND q.id=(SELECT min(id) FROM workflow_queue WHERE sub=?1 AND key=q.key)
			AND NOT (q.run=?2 AND q.boot=?3 AND q.id IN (SELECT value FROM json_each(?5)))
			ORDER BY q.id LIMIT ?4`, run.Subscription, run.ID, boot, limit, string(heldRows))
		if err != nil {
			return err
		}
		type claimed struct {
			row       int64
			key, body string
			handovers int
		}
		var claims []claimed
		for rows.Next() {
			var c claimed
			if err := rows.Scan(&c.row, &c.key, &c.body, &c.handovers); err != nil {
				rows.Close()
				return err
			}
			claims = append(claims, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range claims {
			if c.handovers >= maxHandOvers {
				first, err := retireEvent(ctx, tx, run, c.row, c.key, true, now)
				if err != nil {
					return err
				}
				dropped = append(dropped, DroppedEvent{Row: c.row, Key: c.key, FirstOfKey: first, HandOvers: c.handovers})
				continue
			}
			if _, err := tx.ExecContext(ctx, "UPDATE workflow_queue SET run=?,boot=?,handovers=handovers+1 WHERE id=?", run.ID, boot, c.row); err != nil {
				return err
			}
			events = append(events, contract.WorkflowEvent{Row: c.row, Key: c.key, Event: json.RawMessage(c.body)})
		}
		return nil
	})
	return events, dropped, err
}

// retireEvent ends a row: it leaves the queue, the steps its handler made leave the journal with their usage added to the
// run's totals, and the counts and the key's failure streak move. It reports whether this failure starts a streak.
func retireEvent(ctx context.Context, tx *sql.Tx, run WorkflowRun, row int64, key string, failed bool, now time.Time) (bool, error) {
	var spent contract.Usage
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(cache_write_tokens),0) FROM workflow_steps WHERE run=? AND event=?", run.ID, row).Scan(&spent.InputTokens, &spent.OutputTokens, &spent.CacheReadTokens, &spent.CacheWriteTokens); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM workflow_steps WHERE run=? AND event=?", run.ID, row); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM workflow_queue WHERE id=?", row); err != nil {
		return false, err
	}
	if err := updateServed(ctx, tx, run.ID, func(served *Served) {
		if failed {
			served.Failed++
		} else {
			served.Handled++
		}
		served.LastEvent = now
		served.Usage = served.Usage.Plus(spent)
	}); err != nil {
		return false, err
	}
	if !failed {
		_, err := tx.ExecContext(ctx, "DELETE FROM workflow_streaks WHERE sub=? AND key=?", run.Subscription, key)
		return false, err
	}
	result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO workflow_streaks(sub,key) VALUES(?,?)", run.Subscription, key)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// FinishEvent acknowledges a row of run: its handler has ended, with failed set when it threw. It reports the row's key and
// whether the failure starts a streak, which the parent hears of once. A row already acknowledged, or handed to another run,
// changes nothing, so a retried acknowledgement is harmless.
func (s *Store) FinishEvent(ctx context.Context, run WorkflowRun, row int64, failed bool, now time.Time) (key string, first bool, err error) {
	err = s.Write(ctx, func(tx *sql.Tx) error {
		first = false
		err := tx.QueryRowContext(ctx, "SELECT key FROM workflow_queue WHERE id=? AND run=?", row, run.ID).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			key = ""
			return nil
		}
		if err != nil {
			return err
		}
		first, err = retireEvent(ctx, tx, run, row, key, failed, now)
		return err
	})
	return key, first, err
}

// SubscriptionWorkflowState reads or writes the state of the key of a row that the run's box is handling. The row decides the
// key, never anything the box says.
func (s *Store) SubscriptionWorkflowState(ctx context.Context, run contract.WorkflowRunID, row int64, value json.RawMessage) (json.RawMessage, error) {
	var result json.RawMessage
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var sub, key string
		if err := tx.QueryRowContext(ctx, "SELECT sub,key FROM workflow_queue WHERE id=? AND run=?", row, run).Scan(&sub, &key); err != nil {
			return err
		}
		if len(value) > 0 {
			if _, err := tx.ExecContext(ctx, "INSERT INTO workflow_state(sub,key,value) VALUES(?,?,?) ON CONFLICT(sub,key) DO UPDATE SET value=excluded.value", sub, key, string(value)); err != nil {
				return err
			}
			result = value
			return nil
		}
		var stored string
		err := tx.QueryRowContext(ctx, "SELECT value FROM workflow_state WHERE sub=? AND key=?", sub, key).Scan(&stored)
		if err == sql.ErrNoRows {
			result = json.RawMessage("null")
			return nil
		}
		result = json.RawMessage(stored)
		return err
	})
	return result, err
}

// UnservedSubscriptions lists the workflow subscriptions that should have a run and have none: enabled, not held, out of
// backoff, with no running service run.
func (s *Store) UnservedSubscriptions(ctx context.Context, now time.Time) ([]Subscription, error) {
	subs, err := loadSubscriptions(ctx, s.Read(), `WHERE s.enabled=1 AND s.hold_until<=? AND EXISTS(SELECT 1 FROM workflow_subscriptions w WHERE w.sub=s.id)
		AND NOT EXISTS(SELECT 1 FROM workflow_runs r WHERE r.sub=s.id AND r.key='' AND r.state='running') ORDER BY s.id`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	var out []Subscription
	for _, sub := range subs {
		if !sub.Held {
			out = append(out, sub)
		}
	}
	return out, nil
}

// UnwantedServiceRuns lists the service runs still running whose subscription is gone or disabled.
func (s *Store) UnwantedServiceRuns(ctx context.Context) ([]WorkflowRun, error) {
	return queryAll(ctx, s.Read(), scanWorkflow, "SELECT "+workflowColumns+" FROM workflow_runs WHERE sub!='' AND key='' AND state='running' AND NOT EXISTS(SELECT 1 FROM subscriptions s WHERE s.id=workflow_runs.sub AND s.enabled=1)")
}

// ServiceRun finds a subscription's running service run, or ErrNotFound.
func (s *Store) ServiceRun(ctx context.Context, sub contract.SubscriptionID) (WorkflowRun, error) {
	return scanWorkflow(s.Read().QueryRowContext(ctx, "SELECT "+workflowColumns+" FROM workflow_runs WHERE sub=? AND key='' AND state='running'", sub))
}

// ServiceKeys counts a subscription's events by key: waiting in the queue, and handed to a box.
func (s *Store) ServiceKeys(ctx context.Context, sub contract.SubscriptionID) ([]contract.WorkflowServiceKey, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT key,COUNT(CASE WHEN run='' THEN 1 END),COUNT(CASE WHEN run!='' THEN 1 END) FROM workflow_queue WHERE sub=? GROUP BY key ORDER BY key", sub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []contract.WorkflowServiceKey{}
	for rows.Next() {
		var key contract.WorkflowServiceKey
		if err := rows.Scan(&key.Key, &key.Waiting, &key.InFlight); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// FailService records the end of a service run that failed: the subscription's failure count, and its backoff, which holds
// the next start. The events are the notice to the parent.
func (s *Store) FailService(ctx context.Context, sub contract.SubscriptionID, outcome HookOutcome, parent contract.SessionID, events ...contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE subscriptions SET failures=?,hold_until=?,last_at=?,last_result=? WHERE id=?", outcome.Failures, millis(outcome.HoldUntil), events[0].Time.UnixMilli(), outcome.Result, sub); err != nil {
			return err
		}
		if _, err := s.AppendEvent(tx, events[0]); err != nil {
			return err
		}
		for _, notice := range events[1:] {
			if err := s.noticeSession(ctx, tx, parent, notice); err != nil {
				return err
			}
		}
		return nil
	})
}

// NoticeSession stores a notice and wakes a session with it.
func (s *Store) NoticeSession(ctx context.Context, to contract.SessionID, notice contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error { return s.noticeSession(ctx, tx, to, notice) })
}

func (s *Store) noticeSession(ctx context.Context, tx *sql.Tx, to contract.SessionID, notice contract.Event) error {
	seq, err := s.AppendEvent(tx, notice)
	if err != nil {
		return err
	}
	return s.InsertDelivery(ctx, tx, seq, notice.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: to}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
}

// Info is what a run page shows of a service: Ended once its run has, else Starting until its box first asked for events. The keys are the caller's.
func (s Served) Info(running bool) *contract.WorkflowService {
	info := &contract.WorkflowService{State: contract.WorkflowServiceStarting, Keys: []contract.WorkflowServiceKey{}, Handled: s.Handled, Failed: s.Failed, LastEvent: s.LastEvent, Usage: s.Usage}
	switch {
	case !running:
		info.State = contract.WorkflowServiceEnded
	case !s.Since.IsZero():
		info.State = contract.WorkflowServiceServing
	}
	return info
}
