package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type accountStatusService struct {
	Service
	called bool
}

func (s *accountStatusService) AccountsConnectStatus(context.Context, contract.Principal, contract.AccountsConnectStatusRequest) (contract.AccountsConnectStatusResponse, *contract.Error) {
	s.called = true
	return contract.AccountsConnectStatusResponse{Status: contract.ConnectConnected}, nil
}

func (s *accountStatusService) AccountsRename(context.Context, contract.Principal, contract.AccountsRenameRequest) (contract.AccountsRenameResponse, *contract.Error) {
	s.called = true
	return contract.AccountsRenameResponse{}, nil
}

func TestAccountRenameRequiresOwnerAdmin(t *testing.T) {
	for name, caller := range map[string]Caller{
		"read-only device": {Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeRead}},
		"session":          {Principal: contract.Principal{Kind: contract.PrincipalSession, Session: "invented"}, Scopes: []contract.Scope{contract.ScopeAdmin}},
	} {
		s := &accountStatusService{}
		w := request(New(s, nil).handler(caller), "/v1/accounts.rename", `{"accountId":"invented","label":"Work"}`)
		if w.Code != http.StatusForbidden || s.called {
			t.Fatalf("%s could rename an account: %d", name, w.Code)
		}
	}
}

func TestAccountCompletionRequiresAdmin(t *testing.T) {
	s := &accountStatusService{}
	caller := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeRead}}
	w := request(New(s, nil).handler(caller), "/v1/accounts.connect_status", `{"attempt":"invented"}`)
	if w.Code != http.StatusForbidden || s.called {
		t.Fatal("read-only device could complete an account")
	}
}
