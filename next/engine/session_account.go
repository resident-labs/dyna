package engine

import (
	"context"
	"slices"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/private"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

// sessionAccounts is what the engine knows about the accounts one session could use.
type sessionAccounts struct {
	bindings    []private.AccountBinding
	credentials map[string]model.Credential
	eligible    []string
	models      []string
}

func (s *AccountActions) sessionAccounts(ctx context.Context, session store.Session) (sessionAccounts, error) {
	return s.accountsForProfile(ctx, session.Room, session.Profile)
}

func (s *AccountActions) accountsForProfile(ctx context.Context, room contract.RoomID, profile contract.ProfileName) (sessionAccounts, error) {
	choice, allowed, err := s.service.profiles.Model(profile)
	if err != nil {
		return sessionAccounts{}, err
	}
	bindings, err := s.service.store.Accounts(ctx)
	if err != nil {
		return sessionAccounts{}, err
	}
	eligible, err := s.service.store.EligibleAccountProxyIDs(ctx, room)
	if err != nil {
		return sessionAccounts{}, err
	}
	credentials, err := s.connections.Credentials(ctx)
	if err != nil {
		return sessionAccounts{}, err
	}
	byID := make(map[string]model.Credential, len(credentials))
	for _, credential := range credentials {
		byID[credential.ID] = credential
	}
	return sessionAccounts{bindings: bindings, credentials: byID, eligible: eligible, models: append(allowed, choice.ID)}, nil
}

// serves reports whether an account can run the session's model. Provider names differ
// between profiles and accounts, and several accounts speak one wire protocol, so the
// models a credential serves are what decide.
func (a sessionAccounts) serves(binding private.AccountBinding) bool {
	credential, ok := a.credentials[binding.ProxyID]
	return ok && slices.ContainsFunc(a.models, func(name string) bool { return slices.Contains(credential.Models, name) })
}

func (s *AccountActions) signedIn(credential model.Credential) bool {
	state := credential.State
	if state == model.CredentialUnknown {
		state = s.quotas.ObservedState(credential.Index)
	}
	return state == model.CredentialConnected || state == model.CredentialUnknown
}

func (a sessionAccounts) choices(s *AccountActions) []contract.SessionAccountChoice {
	choices := []contract.SessionAccountChoice{}
	for _, binding := range a.bindings {
		if a.serves(binding) && slices.Contains(a.eligible, binding.ProxyID) && s.signedIn(a.credentials[binding.ProxyID]) {
			choices = append(choices, contract.SessionAccountChoice{ID: binding.Account.ID, Label: binding.Account.Label})
		}
	}
	return choices
}

// SessionAccount reads a session's choice and whether it works right now. The proxy
// decides that per request; this says the same thing from the same routing order.
func (s *AccountActions) SessionAccount(ctx context.Context, caller contract.Principal, req contract.SessionAccountRequest) (contract.SessionAccountResponse, *contract.Error) {
	var response contract.SessionAccountResponse
	if caller.Kind != contract.PrincipalOwner {
		return response, forbidden()
	}
	session, problem := s.service.managedSession(ctx, caller, req.Session)
	if problem != nil {
		return response, problem
	}
	pin, err := s.service.store.SessionAccountPin(ctx, session.ID)
	if err != nil {
		return response, failure(err)
	}
	accounts, err := s.sessionAccounts(ctx, session)
	if err != nil {
		return response, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable"}
	}
	response.Choices = accounts.choices(s)
	response.Account = pin
	if pin == "" {
		return response, nil
	}
	index := slices.IndexFunc(accounts.bindings, func(binding private.AccountBinding) bool { return binding.Account.ID == pin })
	if index < 0 {
		response.Issue = contract.SessionAccountRemoved
		return response, nil
	}
	binding := accounts.bindings[index]
	response.Label = binding.Account.Label
	response.Issue = s.pinIssue(ctx, accounts, binding)
	return response, nil
}

func (s *AccountActions) pinIssue(ctx context.Context, accounts sessionAccounts, binding private.AccountBinding) contract.SessionAccountIssue {
	credential, ok := accounts.credentials[binding.ProxyID]
	if !ok || !s.signedIn(credential) {
		return contract.SessionAccountSignedOut
	}
	if !accounts.serves(binding) || !slices.Contains(accounts.eligible, binding.ProxyID) {
		return contract.SessionAccountUnavailable
	}
	routing, ok := s.quotas.Routing(ctx)
	if !ok {
		return ""
	}
	switch place := routing[credential.Index]; {
	case place.Spent:
		return contract.SessionAccountSpent
	case place.Rank == 0:
		return contract.SessionAccountUnavailable
	}
	return ""
}

// SessionSetAccount chooses an account for a session, or Automatic. The method is
// owner-only in the registry too, so a box cannot reach it.
func (s *AccountActions) SessionSetAccount(ctx context.Context, caller contract.Principal, req contract.SessionSetAccountRequest) (contract.SessionSetAccountResponse, *contract.Error) {
	var response contract.SessionSetAccountResponse
	if caller.Kind != contract.PrincipalOwner {
		return response, forbidden()
	}
	session, problem := s.service.managedSession(ctx, caller, req.Session)
	if problem != nil {
		return response, problem
	}
	if req.Account != "" {
		accounts, err := s.sessionAccounts(ctx, session)
		if err != nil {
			return response, &contract.Error{Code: contract.ErrUnavailable, Message: "accounts unavailable"}
		}
		if !slices.ContainsFunc(accounts.choices(s), func(choice contract.SessionAccountChoice) bool { return choice.ID == req.Account }) {
			return response, invalid("this account cannot serve the session")
		}
	}
	if err := s.service.store.SetSessionAccountPin(ctx, session.ID, req.Account); err != nil {
		return response, failure(err)
	}
	return response, nil
}

// sessionsMoved tells account choices that sessions now run on profile. A caller must not
// hold the profile lock: this takes it, and it reads the proxy.
func (s *Service) sessionsMoved(ctx context.Context, sessions []store.Session, profile contract.ProfileName) {
	if s.profileMoved != nil {
		s.profileMoved(ctx, sessions, profile)
	}
}

// dropUnservedPins runs after sessions change model. A choice the new model cannot use
// goes, so returning to the old provider later does not bring it back. It keeps a choice
// for an account that is gone, which the row reports. If the accounts cannot be read the
// choice stays, and the proxy ignores an account that cannot serve the model. Only a
// session with a choice reads the proxy.
func (s *AccountActions) dropUnservedPins(ctx context.Context, sessions []store.Session, profile contract.ProfileName) {
	for _, session := range sessions {
		pin, err := s.service.store.SessionAccountPin(ctx, session.ID)
		if err != nil || pin == "" {
			continue
		}
		accounts, err := s.accountsForProfile(ctx, session.Room, profile)
		if err != nil {
			continue
		}
		index := slices.IndexFunc(accounts.bindings, func(binding private.AccountBinding) bool { return binding.Account.ID == pin })
		if index < 0 || accounts.serves(accounts.bindings[index]) {
			continue
		}
		_ = s.service.store.SetSessionAccountPin(ctx, session.ID, "")
	}
}
