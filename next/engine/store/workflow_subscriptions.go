package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// queueWorkflow splits a batched PR notification at the key boundary before
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
	if changed, ok := event.Body.(contract.IssueChanged); ok {
		events = nil
		for _, change := range changed.Changes {
			one := event
			one.Body = contract.IssueChanged{Session: changed.Session, Changes: []contract.IssueChange{change}}
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
					switch kind {
					case contract.PrChangeComment, contract.PrChangeChangesRequested, contract.PrChangeApproved, contract.PrChangeMetadata:
						allowed = !slices.Contains(c.OwnKinds, kind)
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
		case contract.IssueChanged:
			// Issues carry actors in Outside; PR review provenance is summarized per kind in OwnKinds.
			c := b.Changes[0]
			repo, number = c.Issue.Repo, c.Issue.Number
			if !sub.IncludeOwn {
				var kinds []contract.PrChangeKind
				for _, kind := range c.Kinds {
					allowed := true
					switch kind {
					case contract.PrChangeComment:
						allowed = slices.ContainsFunc(c.Comments, func(comment contract.PrComment) bool { return comment.Outside })
					case contract.PrChangeMetadata:
						allowed = slices.ContainsFunc(c.Actions, func(action contract.GitHubItem) bool { return action.Outside })
					}
					if allowed {
						kinds = append(kinds, kind)
					}
				}
				if len(kinds) == 0 {
					continue
				}
				c.Kinds = kinds
				event.Body = contract.IssueChanged{Session: b.Session, Changes: []contract.IssueChange{c}}
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
		key := strings.NewReplacer("{repo}", string(repo), "{number}", strconv.Itoa(number)).Replace(sub.Key)
		body, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if sub.Latest {
			result, err := tx.ExecContext(ctx, "UPDATE workflow_queue SET seq=?,body=? WHERE sub=? AND key=? AND run=''", event.Seq, string(body), sub.ID, key)
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
		if _, err := tx.ExecContext(ctx, "INSERT INTO workflow_queue(sub,key,seq,body) SELECT ?1,?2,?3,?4 WHERE (SELECT count(*) FROM workflow_queue WHERE sub=?1 AND run IN ('','starting'))<?5", sub.ID, key, event.Seq, string(body), maxQueuedRuns); err != nil {
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
func (s *Store) QueuedWorkflows(ctx context.Context, now time.Time) ([]QueuedWorkflow, error) {
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
		if sub.Held || sub.HoldUntil.After(now) {
			continue
		}
		rows, err := s.Read().QueryContext(ctx, `SELECT id,key FROM workflow_queue q WHERE sub=? AND run IN ('','starting') AND NOT EXISTS(SELECT 1 FROM workflow_queue a WHERE a.sub=q.sub AND a.key=q.key AND (a.id!=q.id AND (a.run!='' OR a.id<q.id))) ORDER BY id`, sub.ID)
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
// never by a key supplied by the box.
func (s *Store) SubscriptionWorkflowState(ctx context.Context, run contract.WorkflowRunID, value json.RawMessage) (json.RawMessage, error) {
	var result json.RawMessage
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var sub, key string
		if err := tx.QueryRowContext(ctx, "SELECT q.sub,q.key FROM workflow_queue q JOIN workflow_runs r ON r.id=q.run WHERE q.run=? AND r.state='running'", run).Scan(&sub, &key); err != nil {
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

// FailSubscriptionWorkflow consumes a refused start like a failed command run:
// its event is not retried, and subsequent events wait out the subscription backoff.
func (s *Store) FailSubscriptionWorkflow(ctx context.Context, queue int64, outcome HookOutcome, parent contract.SessionID, events ...contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var sub contract.SubscriptionID
		if err := tx.QueryRowContext(ctx, "DELETE FROM workflow_queue WHERE id=? AND run='starting' RETURNING sub", queue).Scan(&sub); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE subscriptions SET failures=?,hold_until=?,last_at=?,last_result=? WHERE id=?", outcome.Failures, millis(outcome.HoldUntil), events[0].Time.UnixMilli(), outcome.Result, sub); err != nil {
			return err
		}
		for i, event := range events {
			seq, err := s.AppendEvent(tx, event)
			if err != nil {
				return err
			}
			if i > 0 {
				if err := s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: parent}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
