package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"strconv"
	"strings"
)

func (s *Service) ownedWorkflowSubscription(ctx context.Context, caller contract.Principal, id contract.SubscriptionID) (store.Subscription, *contract.Error) {
	sub, problem := s.ownedSubscription(ctx, caller, id)
	if problem != nil {
		return sub, problem
	}
	if sub.Workflow == nil {
		return sub, invalid("this subscription does not start workflows; add a room subscription with workflow first")
	}
	return sub, nil
}

func (s *Service) SubscriptionUpdate(ctx context.Context, caller contract.Principal, req contract.SubscriptionUpdateRequest) (contract.SubscriptionUpdateResponse, *contract.Error) {
	sub, problem := s.ownedWorkflowSubscription(ctx, caller, req.ID)
	if problem != nil {
		return contract.SubscriptionUpdateResponse{}, problem
	}
	if req.Workflow != nil {
		sub.Workflow = req.Workflow
	}
	if req.Where != nil {
		sub.Where = *req.Where
	}
	if req.Key != nil {
		sub.Key = *req.Key
	}
	if req.Latest != nil {
		sub.Latest = *req.Latest
	}
	if req.Parent != nil {
		sub.Parent = *req.Parent
	}
	if problem := checkSubscriptionShape(contract.SubscriptionAddRequest{Scope: sub.Scope, On: sub.On, Every: sub.Every, Where: sub.Where, Workflow: sub.Workflow, Key: sub.Key, Latest: sub.Latest, Parent: sub.Parent, IncludeOwn: sub.IncludeOwn}); problem != nil {
		return contract.SubscriptionUpdateResponse{}, problem
	}
	if _, _, problem := s.workflowScript(ctx, sub.Room, sub.Workflow.Script); problem != nil {
		return contract.SubscriptionUpdateResponse{}, problem
	}
	if sub.Parent != "" {
		target, problem := s.subscriptionSession(ctx, caller, sub.Room, string(sub.Parent))
		if problem != nil {
			return contract.SubscriptionUpdateResponse{}, problem
		}
		if target.Room != sub.Room {
			return contract.SubscriptionUpdateResponse{}, invalid("parent must be in this room")
		}
		sub.Parent = target.ID
	}
	if err := s.store.UpdateWorkflowSubscription(ctx, sub.Subscription); err != nil {
		return contract.SubscriptionUpdateResponse{}, failure(err)
	}
	s.pokeSubscriptions()
	return contract.SubscriptionUpdateResponse{}, nil
}

func (s *Service) SubscriptionState(ctx context.Context, caller contract.Principal, req contract.SubscriptionStateRequest) (contract.SubscriptionStateResponse, *contract.Error) {
	_, problem := s.ownedWorkflowSubscription(ctx, caller, req.ID)
	if problem != nil {
		return contract.SubscriptionStateResponse{}, problem
	}
	if len(req.Value) > 0 && req.Key == nil {
		return contract.SubscriptionStateResponse{}, invalid("set key to write or reset one key's state; omit value to list")
	}
	if len(req.Value) > 65536 || len(req.Value) > 0 && !json.Valid(req.Value) {
		return contract.SubscriptionStateResponse{}, invalid("state is JSON of at most 64 KiB")
	}
	states, err := s.store.ManageSubscriptionState(ctx, req.ID, req.Key, bytes.TrimSpace(req.Value))
	if err != nil {
		return contract.SubscriptionStateResponse{}, failure(err)
	}
	return contract.SubscriptionStateResponse{States: states}, nil
}

func (s *Service) SubscriptionFire(ctx context.Context, caller contract.Principal, req contract.SubscriptionFireRequest) (contract.SubscriptionFireResponse, *contract.Error) {
	sub, problem := s.ownedWorkflowSubscription(ctx, caller, req.ID)
	if problem != nil {
		return contract.SubscriptionFireResponse{}, problem
	}
	var body contract.EventBody = contract.HookDue{Subscription: sub.ID}
	if len(req.Event) > 0 {
		if len(req.Event) > 1<<20 {
			return contract.SubscriptionFireResponse{}, invalid("event is at most 1 MiB")
		}
		var event contract.Event
		if err := json.Unmarshal(req.Event, &event); err != nil {
			return contract.SubscriptionFireResponse{}, invalid("event needs a top-level type and body: " + err.Error())
		}
		body = event.Body
	}
	var repo contract.RepoName
	var number int
	switch b := body.(type) {
	case contract.PrChanged:
		if len(b.Changes) != 1 {
			return contract.SubscriptionFireResponse{}, invalid("fire takes one PR change in body.changes")
		}
		repo, number = b.Changes[0].PR.Repo, b.Changes[0].PR.Number
	case contract.IssueChanged:
		if len(b.Changes) != 1 {
			return contract.SubscriptionFireResponse{}, invalid("fire takes one issue change in body.changes")
		}
		repo, number = b.Changes[0].Issue.Repo, b.Changes[0].Issue.Number
	case contract.CIFinished:
		repo, number = b.Repo, b.Number
	case contract.GitMainUpdated:
		repo = b.Repo
	case contract.GitMainCIFinished:
		repo = b.Repo
	case interface{ Item() contract.GitHubItem }:
		item := b.Item()
		repo, number = item.Repo, item.Number
	}
	key := strings.NewReplacer("{repo}", string(repo), "{number}", strconv.Itoa(number)).Replace(sub.Key)
	if req.Key != nil {
		key = *req.Key
	}
	if len(key) > 200 {
		return contract.SubscriptionFireResponse{}, invalid("key is at most 200 characters")
	}
	event, err := s.event(sub.Room, caller, body)
	if err != nil {
		return contract.SubscriptionFireResponse{}, failure(err)
	}
	queued, err := s.store.FireSubscriptionWorkflow(ctx, sub, key, event)
	if err != nil {
		return contract.SubscriptionFireResponse{}, failure(err)
	}
	if !queued {
		return contract.SubscriptionFireResponse{}, invalid("this subscription already has 256 waiting events; wait for a run to finish and fire again")
	}
	s.pokeSubscriptions()
	return contract.SubscriptionFireResponse{}, nil
}
