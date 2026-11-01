package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type pinService struct {
	*fakeService
	reached int
}

func (s *pinService) SessionAccount(context.Context, contract.Principal, contract.SessionAccountRequest) (contract.SessionAccountResponse, *contract.Error) {
	s.reached++
	return contract.SessionAccountResponse{}, nil
}

func (s *pinService) SessionSetAccount(context.Context, contract.Principal, contract.SessionSetAccountRequest) (contract.SessionSetAccountResponse, *contract.Error) {
	s.reached++
	return contract.SessionSetAccountResponse{}, nil
}

// The access table refuses a box before the service is reached, for its own session too.
func TestBoxCannotCallSessionAccountMethods(t *testing.T) {
	service := &pinService{fakeService: &fakeService{}}
	h := New(service, nil).handler(boxCaller())
	for _, path := range []string{"/v1/session.account", "/v1/session.set_account"} {
		if got := request(h, path, `{"session":"session-a","account":"account"}`); got.Code != http.StatusForbidden {
			t.Fatalf("%s: status %d", path, got.Code)
		}
	}
	if service.reached != 0 {
		t.Fatalf("a box reached the service %d times", service.reached)
	}
	owner := New(service, nil).handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}})
	if got := request(owner, "/v1/session.set_account", `{"session":"session-a","account":"account"}`); got.Code != http.StatusOK {
		t.Fatalf("owner: status %d", got.Code)
	}
}
