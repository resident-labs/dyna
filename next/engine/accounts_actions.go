package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

var errAccountAction = errors.New("account action unavailable")

type accountActionResult struct {
	connect  contract.AccountsConnectResponse
	key      contract.AccountsSetKeyResponse
	imported contract.AccountsImportOAuthResponse
	err      error
}

type AccountActions struct {
	service     *Service
	connections *model.Connections
	quotas      *model.Quotas
	removeMu    sync.Mutex
}

func NewAccountActions(service *Service, connections *model.Connections, quotas *model.Quotas) *AccountActions {
	return &AccountActions{service: service, connections: connections, quotas: quotas}
}

func (a *AccountActions) Entry() approve.Entry {
	return approve.Entry{
		Global:    true,
		Ephemeral: true,
		Scope:     contract.ScopeAdmin,
		Class:     func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassOwner },
		Card: func(_ context.Context, args contract.OpArgs, _ contract.Principal) contract.Card {
			name := "Account action"
			switch args.(contract.AccountsActionArgs).Action {
			case contract.AccountActionConnect:
				name = "Start account connection"
			case contract.AccountActionReconnect:
				name = "Reconnect account"
			case contract.AccountActionCallback:
				name = "Complete account authorization"
			case contract.AccountActionComplete:
				name = "Check account connection"
			case contract.AccountActionImportOAuth:
				name = "Import account sign-in"
			case contract.AccountActionSetKey:
				name = "Save account key"
			case contract.AccountActionRemove:
				name = "Remove account"
			case contract.AccountActionSetLimits:
				name = "Set account room limits"
			}
			return contract.Card{Title: name, Target: contract.Target{Kind: contract.TargetAccount}}
		},
		Check: func(context.Context, contract.OpArgs, contract.Principal) error { return nil },
		Execute: func(context.Context, contract.OpID, contract.OpArgs, contract.Principal) contract.OpResult {
			return contract.OpResult{Error: "account action interrupted", Cause: contract.CauseInterrupted}
		},
		Decode: func(raw json.RawMessage) (contract.OpArgs, error) {
			var args contract.AccountsActionArgs
			err := json.Unmarshal(raw, &args)
			return args, err
		},
	}
}

func (s *AccountActions) runAccountAction(ctx context.Context, caller contract.Principal, kind contract.AccountAction, account contract.AccountID, run func(context.Context) accountActionResult) (accountActionResult, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return accountActionResult{}, &contract.Error{Code: contract.ErrForbidden, Message: "forbidden"}
	}
	resultCh := make(chan accountActionResult, 1)
	op, err := s.service.approvals.SubmitEphemeral(ctx, caller, contract.Operation{Args: contract.AccountsActionArgs{Action: kind, Account: account}}, func(ctx context.Context) contract.OpResult {
		result := run(ctx)
		resultCh <- result
		if result.err != nil {
			return contract.OpResult{Error: "account action failed"}
		}
		return contract.OpResult{}
	})
	if err != nil {
		return accountActionResult{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account action unavailable"}
	}
	if op.State == contract.OpFailed {
		select {
		case result := <-resultCh:
			if errors.Is(result.err, model.ErrReconnectUnavailable) {
				return accountActionResult{}, &contract.Error{Code: contract.ErrReconnectUnavailable, Message: "reconnect unavailable"}
			}
			if errors.Is(result.err, store.ErrRoomDeleting) {
				return accountActionResult{}, failure(result.err)
			}
		default:
		}
		return accountActionResult{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account action failed"}
	}
	select {
	case result := <-resultCh:
		if result.err != nil {
			return accountActionResult{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account action failed"}
		}
		return result, nil
	case <-ctx.Done():
		return accountActionResult{}, &contract.Error{Code: contract.ErrUnavailable, Message: "account action unavailable"}
	}
}

func (s *AccountActions) AccountsList(ctx context.Context, caller contract.Principal, _ contract.AccountsListRequest) (contract.AccountsListResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.AccountsListResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "forbidden"}
	}
	bindings, err := s.service.store.Accounts(ctx)
	if err != nil {
		return contract.AccountsListResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable"}
	}
	accounts := make([]contract.Account, len(bindings))
	credentials, err := s.connections.Credentials(ctx)
	states := make(map[string]model.CredentialState, len(credentials))
	for _, credential := range credentials {
		state := credential.State
		if state == model.CredentialUnknown {
			state = s.quotas.ObservedState(credential.Index)
		}
		states[credential.ID] = state
	}
	for i, binding := range bindings {
		accounts[i] = binding.Account
		if err == nil {
			switch states[binding.ProxyID] {
			case model.CredentialConnected:
				accounts[i].State = contract.AccountConnected
			case model.CredentialExpired:
				accounts[i].State, accounts[i].Reason = contract.AccountSignInExpired, contract.AccountSignInRejected
			case model.CredentialFailed:
				accounts[i].State, accounts[i].Reason = contract.AccountFailedState, contract.AccountProxyFailed
			case "":
				accounts[i].State, accounts[i].Reason = contract.AccountFailedState, contract.AccountCredentialMissing
			}
		}
	}
	return contract.AccountsListResponse{Accounts: accounts, Providers: model.AccountProviders()}, nil
}

func (s *AccountActions) AccountsConnect(ctx context.Context, caller contract.Principal, req contract.AccountsConnectRequest) (contract.AccountsConnectResponse, *contract.Error) {
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" || len(req.Label) > 128 || strings.ContainsAny(req.Label, "\r\n@") {
		return contract.AccountsConnectResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "invalid account label"}
	}
	result, problem := s.runAccountAction(ctx, caller, contract.AccountActionConnect, "", func(ctx context.Context) accountActionResult {
		out, err := s.connections.Start(ctx, req)
		return accountActionResult{connect: out, err: err}
	})
	return result.connect, problem
}

func (s *AccountActions) AccountsReconnect(ctx context.Context, caller contract.Principal, req contract.AccountsReconnectRequest) (contract.AccountsReconnectResponse, *contract.Error) {
	result, problem := s.runAccountAction(ctx, caller, contract.AccountActionReconnect, req.AccountID, func(ctx context.Context) accountActionResult {
		s.removeMu.Lock()
		defer s.removeMu.Unlock()
		bindings, err := s.service.store.Accounts(ctx)
		if err != nil {
			return accountActionResult{err: err}
		}
		for _, binding := range bindings {
			if binding.Account.ID == req.AccountID {
				out, err := s.connections.Reconnect(ctx, req.AccountID, binding.ProxyID, binding.Account.Provider)
				return accountActionResult{connect: out, err: err}
			}
		}
		return accountActionResult{err: errAccountAction}
	})
	return contract.AccountsReconnectResponse(result.connect), problem
}

func (s *AccountActions) AccountsRemove(ctx context.Context, caller contract.Principal, req contract.AccountsRemoveRequest) (contract.AccountsRemoveResponse, *contract.Error) {
	_, problem := s.runAccountAction(ctx, caller, contract.AccountActionRemove, req.AccountID, func(ctx context.Context) accountActionResult {
		s.removeMu.Lock()
		defer s.removeMu.Unlock()
		s.connections.CancelReconnect(req.AccountID)
		id, err := s.service.store.AccountProxyID(ctx, req.AccountID)
		if err == nil {
			err = s.connections.RemoveCredential(ctx, id)
		}
		if err == nil {
			s.quotas.Clear()
			err = s.service.store.RemoveAccount(ctx, req.AccountID)
		}
		return accountActionResult{err: err}
	})
	return contract.AccountsRemoveResponse{}, problem
}

func (s *AccountActions) AccountsSetKey(ctx context.Context, caller contract.Principal, req contract.AccountsSetKeyRequest) (contract.AccountsSetKeyResponse, *contract.Error) {
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" || len(req.Label) > 128 || strings.ContainsAny(req.Label, "\r\n@") || strings.TrimSpace(req.Key) == "" || len(req.Key) > 4096 {
		return contract.AccountsSetKeyResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "invalid account input"}
	}
	result, problem := s.runAccountAction(ctx, caller, contract.AccountActionSetKey, "", func(ctx context.Context) accountActionResult {
		out, err := s.connections.SetKey(ctx, req)
		return accountActionResult{key: out, err: err}
	})
	return result.key, problem
}

func (s *AccountActions) AccountsConnectCallback(ctx context.Context, caller contract.Principal, req contract.AccountsConnectCallbackRequest) (contract.AccountsConnectCallbackResponse, *contract.Error) {
	_, problem := s.runAccountAction(ctx, caller, contract.AccountActionCallback, "", func(ctx context.Context) accountActionResult {
		return accountActionResult{err: s.connections.Callback(ctx, req)}
	})
	return contract.AccountsConnectCallbackResponse{}, problem
}

// CompleteOAuth commits a private proxy completion without a browser request.
func (s *AccountActions) CompleteOAuth(ctx context.Context, completed model.OAuthCompletion) error {
	s.removeMu.Lock()
	defer s.removeMu.Unlock()
	return s.connections.Complete(ctx, completed, func(commit func() error) error {
		_, problem := s.runAccountAction(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.AccountActionComplete, "", func(context.Context) accountActionResult { return accountActionResult{err: commit()} })
		if problem != nil {
			return errAccountAction
		}
		return nil
	})
}

func (s *AccountActions) AccountsConnectStatus(ctx context.Context, caller contract.Principal, req contract.AccountsConnectStatusRequest) (contract.AccountsConnectStatusResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.AccountsConnectStatusResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "forbidden"}
	}
	s.removeMu.Lock()
	defer s.removeMu.Unlock()
	out, err := s.connections.Status(ctx, req, func(commit func() error) error {
		_, problem := s.runAccountAction(ctx, caller, contract.AccountActionComplete, "", func(context.Context) accountActionResult {
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

func (s *AccountActions) AccountsSetLimits(ctx context.Context, caller contract.Principal, req contract.AccountsSetLimitsRequest) (contract.AccountsSetLimitsResponse, *contract.Error) {
	_, problem := s.runAccountAction(ctx, caller, contract.AccountActionSetLimits, req.AccountID, func(ctx context.Context) accountActionResult {
		return accountActionResult{err: s.service.store.SetAccountRooms(ctx, req.AccountID, req.Rooms)}
	})
	return contract.AccountsSetLimitsResponse{}, problem
}

func (s *AccountActions) AccountsImportOAuth(ctx context.Context, caller contract.Principal, req contract.AccountsImportOAuthRequest) (contract.AccountsImportOAuthResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.AccountsImportOAuthResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "forbidden"}
	}
	req.Label = strings.TrimSpace(req.Label)
	if _, ok := model.OAuthProxyProvider(req.Provider); !ok || req.Label == "" || len(req.Label) > 128 || strings.ContainsAny(req.Label, "\r\n@") || req.ExpiresAt.Unix() <= 0 || len(req.Credential) == 0 || len(req.Credential) > 16384 {
		return contract.AccountsImportOAuthResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "invalid account input"}
	}
	result, problem := s.runAccountAction(ctx, caller, contract.AccountActionImportOAuth, "", func(ctx context.Context) accountActionResult {
		out, err := s.connections.ImportOAuth(ctx, req)
		return accountActionResult{imported: out, err: err}
	})
	return result.imported, problem
}
