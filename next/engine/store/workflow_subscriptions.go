package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// queueWorkflow splits a batched PR notification at the subject boundary before
// applying filters. Different PRs in one poll must never share a state key.
func (s *Store) queueWorkflow(ctx context.Context, tx *sql.Tx, sub Subscription, event contract.Event) error {
	events := []contract.Event{event}
	if changed, ok := event.Body.(contract.PrChanged); ok {
		events = nil
		for _, change := range changed.Changes {
			one := event
			one.Body = contract.PrChanged{Session: changed.Session, Changes: []contract.PrChange{change}}
			events = append(events, one)
		}
	}
	for _, event := range events {
		var repo contract.RepoName
		var number int
		switch b := event.Body.(type) {
		case contract.PrChanged:
			c := b.Changes[0]
			repo, number = c.PR.Repo, c.PR.Number
			if !sub.IncludeOwn {
				var kinds []contract.PrChangeKind
				for _, kind := range c.Kinds {
					allowed := true
					switch string(kind) {
					case "comment", "changes_requested", "approved":
						allowed = false
						for _, comment := range c.Comments {
							allowed = allowed || comment.Outside
						}
					case "metadata":
						allowed = false
						for _, action := range c.Actions {
							allowed = allowed || action.Outside
						}
					}
					if allowed {
						kinds = append(kinds, kind)
					}
				}
				if len(kinds) == 0 {
					continue
				}
				c.Kinds = kinds
				event.Body = contract.PrChanged{Session: b.Session, Changes: []contract.PrChange{c}}
			}
		case contract.CIFinished:
			repo, number = b.Repo, b.Number
		case contract.GitMainUpdated:
			repo = b.Repo
		case contract.GitMainCIFinished:
			repo = b.Repo
		case interface{ Item() contract.GitHubItem }:
			item := b.Item()
			repo, number = item.Repo, item.Number
			if !sub.IncludeOwn && !item.Outside {
				switch item.Action {
				case contract.GitHubOpened, contract.GitHubMerged, contract.GitHubClosed, contract.GitHubReopened, contract.GitHubForcePushed, contract.GitHubReadyForReview, contract.GitHubConvertedToDraft:
				default:
					continue
				}
			}
		}
		if !contract.MatchesWhere(sub.Where, event.Body) {
			continue
		}
		subject := strings.NewReplacer("{repo}", string(repo), "{number}", strconv.Itoa(number)).Replace(sub.Key)
		body, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if sub.Once {
			result, err := tx.ExecContext(ctx, "UPDATE workflow_queue SET seq=?,body=? WHERE sub=? AND subject=? AND run=''", event.Seq, string(body), sub.ID, subject)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n > 0 {
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO workflow_queue(sub,subject,seq,body) SELECT ?1,?2,?3,?4 WHERE (SELECT count(*) FROM workflow_queue WHERE sub=?1 AND run IN ('','starting'))<?5", sub.ID, subject, event.Seq, string(body), maxQueuedRuns); err != nil {
			return err
		}
	}
	return nil
}

type QueuedWorkflow struct {
	ID           int64
	Subscription Subscription
	Key          string
}

// QueuedWorkflows returns only the oldest pending event for each idle key.
func (s *Store) QueuedWorkflows(ctx context.Context) ([]QueuedWorkflow, error) {
	// Terminal runs release their key; state remains until the subscription is removed.
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM workflow_queue WHERE run!='' AND EXISTS(SELECT 1 FROM workflow_runs r WHERE r.id=workflow_queue.run AND r.state!='running')")
		return err
	}); err != nil {
		return nil, err
	}
	subs, err := loadSubscriptions(ctx, s.Read(), "WHERE s.enabled=1 AND EXISTS(SELECT 1 FROM workflow_subscriptions w WHERE w.sub=s.id)")
	if err != nil {
		return nil, err
	}
	var out []QueuedWorkflow
	for _, sub := range subs {
		if sub.Held {
			continue
		}
		rows, err := s.Read().QueryContext(ctx, `SELECT id,subject FROM workflow_queue q WHERE sub=? AND run IN ('','starting') AND NOT EXISTS(SELECT 1 FROM workflow_queue a WHERE a.sub=q.sub AND a.subject=q.subject AND (a.id!=q.id AND (a.run!='' OR a.id<q.id))) ORDER BY id`, sub.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			q := QueuedWorkflow{Subscription: sub}
			if err := rows.Scan(&q.ID, &q.Key); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, q)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) ClaimSubscriptionWorkflow(ctx context.Context, queue int64) (json.RawMessage, error) {
	var body string
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE workflow_queue SET run='starting' WHERE id=? AND run=''", queue)
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT body FROM workflow_queue WHERE id=?", queue).Scan(&body)
	})
	return json.RawMessage(body), err
}

// SubscriptionWorkflowState authorizes state access by the active run's queue row,
// never by a subject supplied by the box.
func (s *Store) SubscriptionWorkflowState(ctx context.Context, run contract.WorkflowRunID, value json.RawMessage) (json.RawMessage, error) {
	var result json.RawMessage
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var sub, subject string
		if err := tx.QueryRowContext(ctx, "SELECT q.sub,q.subject FROM workflow_queue q JOIN workflow_runs r ON r.id=q.run WHERE q.run=? AND r.state='running'", run).Scan(&sub, &subject); err != nil {
			return err
		}
		if len(value) > 0 {
			if _, err := tx.ExecContext(ctx, "INSERT INTO workflow_state(sub,subject,value) VALUES(?,?,?) ON CONFLICT(sub,subject) DO UPDATE SET value=excluded.value", sub, subject, string(value)); err != nil {
				return err
			}
			result = value
			return nil
		}
		var stored string
		err := tx.QueryRowContext(ctx, "SELECT value FROM workflow_state WHERE sub=? AND subject=?", sub, subject).Scan(&stored)
		if err == sql.ErrNoRows {
			result = json.RawMessage("null")
			return nil
		}
		result = json.RawMessage(stored)
		return err
	})
	return result, err
}

func (s *Store) QueueScheduledWorkflow(ctx context.Context, sub Subscription, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE subscriptions SET due_at=? WHERE id=? AND enabled=1 AND due_at=?", nextDue(sub, event.Time), sub.ID, millis(sub.DueAt))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		event.Seq = seq
		return s.queueWorkflow(ctx, tx, sub, event)
	})
}
