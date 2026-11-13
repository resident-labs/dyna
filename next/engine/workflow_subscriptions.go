package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) fireWorkflowSubscriptions(ctx context.Context) error {
	queued, err := s.store.QueuedWorkflows(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, q := range queued {
		sub := q.Subscription
		report := sub.Report
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
			return err
		}
		args := map[string]json.RawMessage{}
		for k, v := range sub.Workflow.Args {
			args[k] = v
		}
		args["event"] = event
		body, err := json.Marshal(args)
		if err != nil {
			return err
		}
		_, problem := s.workflowRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: report}, contract.WorkflowRunRequest{Room: sub.Room, Script: sub.Workflow.Script, Args: string(body), ClientID: fmt.Sprintf("subscription-%d", q.ID)}, q.ID)
		if problem != nil {
			failures = append(failures, fmt.Errorf("subscription %s: %s", sub.ID, problem.Message))
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
