package runtime

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type fakeRegistrar struct{ called bool }

func (f *fakeRegistrar) AddHost(context.Context, contract.HostAddRequest) error {
	f.called = true
	return nil
}

func TestHostAddRequiresOwnerAndConfirmation(t *testing.T) {
	f := &fakeRegistrar{}
	s := serviceWithHosts{boxes: f}
	_, err := s.HostAdd(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostAddRequest{Name: "host", SSH: "user@host"})
	if err == nil || err.Code != contract.ErrInvalid || f.called {
		t.Fatalf("error=%v called=%v", err, f.called)
	}
}
