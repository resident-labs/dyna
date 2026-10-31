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
func TestAccountCompletionRequiresAdmin(t *testing.T) {
	s := &accountStatusService{}
	caller := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeRead}}
	w := request(New(s, nil).handler(caller), "/v1/accounts.connect_status", `{"attempt":"invented"}`)
	if w.Code != http.StatusForbidden || s.called {
		t.Fatal("read-only device could complete an account")
	}
}
