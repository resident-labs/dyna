package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/model"
)

var errAccountAction = errors.New("account action unavailable")

type accountActionResult struct {
	connect contract.AccountsConnectResponse
	key     contract.AccountsSetKeyResponse
	err     error
}

type accountAction struct {
	kind   contract.AccountAction
	run    func(context.Context) accountActionResult
	result chan accountActionResult
}

type AccountActions struct {
	service     *Service
	mu          sync.Mutex
	pending     map[string]accountAction
	connections *model.Connections
}

func NewAccountActions(service *Service, connections *model.Connections) *AccountActions {
	return &AccountActions{service: service, connections: connections, pending: make(map[string]accountAction)}
}

func (a *AccountActions) Entry() approve.Entry {
	return approve.Entry{
		Global: true,
		Scope:  contract.ScopeAdmin,
		Class:  func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassOwner },
		Card: func(_ context.Context, args contract.OpArgs, _ contract.Principal) contract.Card {
			name := "Account action"
			switch args.(contract.AccountsActionArgs).Action {
			case contract.AccountActionConnect:
				name = "Start account connection"
			case contract.AccountActionCallback:
				name = "Complete account authorization"
			case contract.AccountActionComplete:
				name = "Check account connection"
			case contract.AccountActionSetKey:
				name = "Save account key"
			}
			return contract.Card{Title: name, Target: contract.Target{Kind: contract.TargetAccount}}
		},
		Check: func(_ context.Context, args contract.OpArgs, who contract.Principal) error {
			action := args.(contract.AccountsActionArgs)
			a.mu.Lock()
			pending, ok := a.pending[action.Ticket]
			a.mu.Unlock()
			if who.Kind != contract.PrincipalOwner || !ok || pending.kind != action.Action {
				return errAccountAction
			}
			return nil
		},
		Execute: func(ctx context.Context, _ contract.OpID, args contract.OpArgs, _ contract.Principal) contract.OpResult {
			action := args.(contract.AccountsActionArgs)
			a.mu.Lock()
			pending, ok := a.pending[action.Ticket]
			delete(a.pending, action.Ticket)
			a.mu.Unlock()
			if !ok {
				return contract.OpResult{Error: "account action unavailable"}
			}
			result := pending.run(ctx)
			pending.result <- result
			if result.err != nil {
				return contract.OpResult{Error: "account action failed"}
			}
			return contract.OpResult{}
		},
		Decode: func(raw json.RawMessage) (contract.OpArgs, error) {
			var args contract.AccountsActionArgs
			err := json.Unmarshal(raw, &args)
			return args, err
		},
	}
}

func (s *AccountActions) runAccountAction(ctx context.Context, caller contract.Principal, kind contract.AccountAction, run func(context.Context) accountActionResult) (accountActionResult, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return accountActionResult{}, &contract.Error{Code: contract.ErrForbidden, Message: "forbidden"}
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return accountActionResult{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account action unavailable"}
	}
	ticket := hex.EncodeToString(id[:])
	action := accountAction{kind: kind, run: run, result: make(chan accountActionResult, 1)}
	s.mu.Lock()
	if len(s.pending) >= 128 {
		s.mu.Unlock()
		return accountActionResult{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account action unavailable"}
	}
	s.pending[ticket] = action
	s.mu.Unlock()
	response, problem := s.service.OpSubmit(ctx, caller, contract.OpSubmitRequest{Operation: contract.Operation{Args: contract.AccountsActionArgs{Action: kind, Ticket: ticket}}})
	if problem != nil {
		s.mu.Lock()
		delete(s.pending, ticket)
		s.mu.Unlock()
		return accountActionResult{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account action unavailable"}
	}
	if response.Operation.State == contract.OpFailed {
		return accountActionResult{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account action failed"}
	}
	select {
	case result := <-action.result:
		if result.err != nil {
			return accountActionResult{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account action failed"}
		}
		return result, nil
	case <-ctx.Done():
		return accountActionResult{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account action unavailable"}
	}
}

func (s *AccountActions) AccountsConnect(ctx context.Context, caller contract.Principal, req contract.AccountsConnectRequest) (contract.AccountsConnectResponse, *contract.Error) {
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" || len(req.Label) > 128 || strings.ContainsAny(req.Label, "\r\n") {
		return contract.AccountsConnectResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "invalid account label"}
	}
	result, problem := s.runAccountAction(ctx, caller, contract.AccountActionConnect, func(ctx context.Context) accountActionResult {
		out, err := s.connections.Start(ctx, req)
		return accountActionResult{connect: out, err: err}
	})
	return result.connect, problem
}

func (s *AccountActions) AccountsSetKey(ctx context.Context, caller contract.Principal, req contract.AccountsSetKeyRequest) (contract.AccountsSetKeyResponse, *contract.Error) {
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" || len(req.Label) > 128 || strings.ContainsAny(req.Label, "\r\n") || strings.TrimSpace(req.Key) == "" || len(req.Key) > 4096 {
		return contract.AccountsSetKeyResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "invalid account input"}
	}
	result, problem := s.runAccountAction(ctx, caller, contract.AccountActionSetKey, func(ctx context.Context) accountActionResult {
		out, err := s.connections.SetKey(ctx, req)
		return accountActionResult{key: out, err: err}
	})
	return result.key, problem
}

func (s *AccountActions) AccountsConnectCallback(ctx context.Context, caller contract.Principal, req contract.AccountsConnectCallbackRequest) (contract.AccountsConnectCallbackResponse, *contract.Error) {
	_, problem := s.runAccountAction(ctx, caller, contract.AccountActionCallback, func(ctx context.Context) accountActionResult {
		return accountActionResult{err: s.connections.Callback(ctx, req)}
	})
	return contract.AccountsConnectCallbackResponse{}, problem
}

func (s *AccountActions) AccountsConnectStatus(ctx context.Context, caller contract.Principal, req contract.AccountsConnectStatusRequest) (contract.AccountsConnectStatusResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.AccountsConnectStatusResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "forbidden"}
	}
	out, err := s.connections.Status(ctx, req, func(commit func() error) error {
		_, problem := s.runAccountAction(ctx, caller, contract.AccountActionComplete, func(context.Context) accountActionResult {
			return accountActionResult{err: commit()}
		})
		if problem != nil {
			return errAccountAction
		}
		return nil
	})
	if err != nil {
		return contract.AccountsConnectStatusResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account connection unavailable"}
	}
	return out, nil
}
