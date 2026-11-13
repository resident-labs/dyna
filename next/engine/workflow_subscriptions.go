package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) fireWorkflowSubscriptions(ctx context.Context) error {
	queued, err := s.store.QueuedWorkflows(ctx, s.now())
	if err != nil {
		return err
	}
	var failures []error
	refused := map[contract.SubscriptionID]bool{}
	for _, q := range queued {
		sub := q.Subscription
		if refused[sub.ID] {
			continue
		}
		report := sub.Report
		if report != "" {
			target, err := s.store.Session(ctx, report)
			if errors.Is(err, store.ErrNotFound) || err == nil && target.Closing {
				report = ""
			} else if err != nil {
				failures = append(failures, err)
				continue
			}
		}
		if report == "" {
			tree, err := s.store.RoomTree(ctx, sub.Room)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			report = tree.Root()
		}
		event, err := s.store.ClaimSubscriptionWorkflow(ctx, q.ID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		args := map[string]json.RawMessage{}
		for k, v := range sub.Workflow.Args {
			args[k] = v
		}
		args["event"] = event
		body, err := json.Marshal(args)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		_, problem := s.workflowRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: report}, contract.WorkflowRunRequest{Room: sub.Room, Script: sub.Workflow.Script, Args: string(body), ClientID: fmt.Sprintf("subscription-%d", q.ID)}, q.ID)
		if problem != nil {
			refused[sub.ID] = true
			now := s.now()
			outcome := store.HookOutcome{Failures: sub.Failures + 1, Result: contract.HookFailed}
			outcome.HoldUntil = now.Add(backoff(sub.Period, outcome.Failures))
			finished, err := s.event(sub.Room, engineSender, contract.HookFinished{Subscription: sub.ID, Result: contract.HookFailed, Cause: contract.CauseStartFailed, Output: problem.Message, Failures: outcome.Failures, RetryAt: &outcome.HoldUntil})
			if err != nil {
				failures = append(failures, err)
				continue
			}
			events := []contract.Event{finished}
			if outcome.Failures == 1 {
				notice, err := s.event(sub.Room, engineSender, contract.NoticePosted{Text: fmt.Sprintf("Workflow subscription %s could not start: %s. Subsequent events wait until %s. Further failures stay in hook.finished until a start succeeds.", sub.ID, problem.Message, outcome.HoldUntil.Format("15:04 MST"))})
				if err != nil {
					failures = append(failures, err)
					continue
				}
				events = append(events, notice)
			}
			failures = append(failures, s.store.FailSubscriptionWorkflow(ctx, q.ID, outcome, report, events...))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) WorkflowState(ctx context.Context, caller contract.Principal, req contract.WorkflowStateRequest) (contract.WorkflowStateResponse, *contract.Error) {
	run, problem := s.callerWorkflow(ctx, caller)
	if problem != nil {
		return contract.WorkflowStateResponse{}, problem
	}
	if run.State != contract.WorkflowRunning {
		return contract.WorkflowStateResponse{}, invalid("state belongs to an active event run")
	}
	if len(req.Value) > 65536 || len(req.Value) > 0 && !json.Valid(req.Value) {
		return contract.WorkflowStateResponse{}, invalid("state is JSON of at most 64 KiB")
	}
	value, err := s.store.SubscriptionWorkflowState(ctx, run.ID, req.Value)
	if err != nil {
		return contract.WorkflowStateResponse{}, failure(err)
	}
	return contract.WorkflowStateResponse{Value: value}, nil
}
