package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

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
		if _, err := s.queueWorkflowEvent(ctx, tx, sub, key, event); err != nil {
			return err
		}
	}
	return nil
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

func (s *Store) queueWorkflowEvent(ctx context.Context, tx *sql.Tx, sub Subscription, key string, event contract.Event) (bool, error) {
	body, err := json.Marshal(event)
	if err != nil {
		return false, err
	}
	if sub.Latest {
		// An update may have enabled latest while several FIFO events were waiting.
		if _, err := tx.ExecContext(ctx, "DELETE FROM workflow_queue WHERE sub=?1 AND key=?2 AND run='' AND id!=(SELECT min(id) FROM workflow_queue WHERE sub=?1 AND key=?2 AND run='')", sub.ID, key); err != nil {
			return false, err
		}
		result, err := tx.ExecContext(ctx, "UPDATE workflow_queue SET seq=?,body=?,handovers=0 WHERE sub=? AND key=? AND run=''", event.Seq, string(body), sub.ID, key)
		if err != nil {
			return false, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO workflow_queue(sub,key,seq,body) SELECT ?1,?2,?3,?4 WHERE (SELECT count(*) FROM workflow_queue WHERE sub=?1 AND run='')<?5", sub.ID, key, event.Seq, string(body), maxQueuedRuns)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (s *Store) FireSubscriptionWorkflow(ctx context.Context, sub Subscription, key string, event contract.Event) (bool, error) {
	var queued bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		queued, err = s.queueWorkflowEvent(ctx, tx, sub, key, event)
		return err
	})
	return queued, err
}

func (s *Store) UpdateWorkflowSubscription(ctx context.Context, sub contract.Subscription) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		args, err := json.Marshal(sub.Workflow.Args)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM subscription_where WHERE sub=?", sub.ID); err != nil {
			return err
		}
		for pos, clause := range sub.Where {
			if _, err := tx.ExecContext(ctx, "INSERT INTO subscription_where(sub,pos,clause) VALUES(?,?,?)", sub.ID, pos, clause); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE workflow_subscriptions SET script=?,args=?,key=?,latest=?,parent=? WHERE sub=?", sub.Workflow.Script, string(args), sub.Key, sub.Latest, sub.Parent, sub.ID)
		return err
	})
}

func (s *Store) ManageSubscriptionState(ctx context.Context, id contract.SubscriptionID, key *string, value json.RawMessage) ([]contract.SubscriptionKeyState, error) {
	var states []contract.SubscriptionKeyState
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if len(value) > 0 {
			if string(value) == "null" {
				if _, err := tx.ExecContext(ctx, "DELETE FROM workflow_state WHERE sub=? AND key=?", id, *key); err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, "INSERT INTO workflow_state(sub,key,value) VALUES(?,?,?) ON CONFLICT(sub,key) DO UPDATE SET value=excluded.value", id, *key, string(value)); err != nil {
					return err
				}
			}
		}
		query := "SELECT key,value FROM workflow_state WHERE sub=?"
		args := []any{id}
		if key != nil {
			query += " AND key=?"
			args = append(args, *key)
		}
		rows, err := tx.QueryContext(ctx, query+" ORDER BY key", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		states = []contract.SubscriptionKeyState{}
		for rows.Next() {
			var state contract.SubscriptionKeyState
			var raw string
			if err := rows.Scan(&state.Key, &raw); err != nil {
				return err
			}
			state.Value = json.RawMessage(raw)
			states = append(states, state)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if key != nil && len(states) == 0 {
			states = append(states, contract.SubscriptionKeyState{Key: *key, Value: json.RawMessage("null")})
		}
		return nil
	})
	return states, err
}
