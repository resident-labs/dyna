package engine

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) ruleWriter(ctx context.Context, caller contract.Principal, room contract.RoomID) *contract.Error {
	if problem := s.roomAccess(ctx, caller, room); problem != nil {
		return problem
	}
	if caller.Kind == contract.PrincipalOwner {
		return nil
	}
	tree, err := s.store.RoomTree(ctx, room)
	if err != nil {
		return failure(err)
	}
	if tree.Root() != caller.Session {
		return forbidden()
	}
	return nil
}

func (s *Service) RuleAdd(ctx context.Context, caller contract.Principal, req contract.RuleAddRequest) (contract.RuleAddResponse, *contract.Error) {
	var response contract.RuleAddResponse
	if problem := s.ruleWriter(ctx, caller, req.Room); problem != nil {
		return response, problem
	}
	if strings.TrimSpace(req.Text) == "" || utf8.RuneCountInString(req.Text) > 2000 {
		return response, invalid("rule text must contain 1 to 2000 characters")
	}
	event, err := s.event(req.Room, caller, contract.RuleAdded{Text: req.Text})
	if err != nil {
		return response, failure(err)
	}
	response.Number, err = s.store.AddRule(ctx, req, caller, event)
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}

func (s *Service) RuleRemove(ctx context.Context, caller contract.Principal, req contract.RuleRemoveRequest) (contract.RuleRemoveResponse, *contract.Error) {
	var response contract.RuleRemoveResponse
	if problem := s.ruleWriter(ctx, caller, req.Room); problem != nil {
		return response, problem
	}
	if req.Number < 1 {
		return response, invalid("rule number must be positive")
	}
	event, err := s.event(req.Room, caller, contract.RuleRemoved{Number: req.Number})
	if err != nil {
		return response, failure(err)
	}
	err = s.store.RemoveRule(ctx, req, event)
	if errors.Is(err, store.ErrRuleNotFound) {
		return response, invalid("no such rule")
	}
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}

func (s *Service) RuleEdit(ctx context.Context, caller contract.Principal, req contract.RuleEditRequest) (contract.RuleEditResponse, *contract.Error) {
	var response contract.RuleEditResponse
	if problem := s.ruleWriter(ctx, caller, req.Room); problem != nil {
		return response, problem
	}
	if strings.TrimSpace(req.Text) == "" || utf8.RuneCountInString(req.Text) > 2000 {
		return response, invalid("rule text must contain 1 to 2000 characters")
	}
	event, err := s.event(req.Room, caller, contract.RuleEdited{Number: req.Number, Text: req.Text})
	if err != nil {
		return response, failure(err)
	}
	err = s.store.EditRule(ctx, req, event)
	if errors.Is(err, store.ErrRuleNotFound) {
		return response, &contract.Error{Code: contract.ErrNotFound, Message: "no such rule"}
	}
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}

func (s *Service) RuleList(ctx context.Context, caller contract.Principal, req contract.RuleListRequest) (contract.RuleListResponse, *contract.Error) {
	var response contract.RuleListResponse
	if problem := s.roomAccess(ctx, caller, req.Room); problem != nil {
		return response, problem
	}
	var err error
	response.Rules, err = s.store.Rules(ctx, req.Room)
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}
