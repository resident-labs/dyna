package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type grantTargetService struct {
	*fakeService
	to []contract.Principal
}

func (s *grantTargetService) OpSubmit(_ context.Context, _ contract.Principal, req contract.OpSubmitRequest) (contract.OpSubmitResponse, *contract.Error) {
	s.to = append(s.to, req.Operation.Args.(contract.HostexecGrantArgs).To)
	return contract.OpSubmitResponse{}, nil
}

// A box's request that names a session without a kind asks for that session. It used to be replaced by the caller, so a
// parent asking for its child was granted its own host again and the child got nothing.
func TestBoxHostGrantKeepsANamedTarget(t *testing.T) {
	service := &grantTargetService{fakeService: &fakeService{}}
	h := New(service, nil).handler(boxCaller())
	for _, to := range []string{`"to":{"session":"child"},`, ""} {
		w := request(h, "/v1/op.submit", `{"operation":{"kind":"hostexec.grant","args":{`+to+`"hosts":["h"],"reason":"x"}}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("%q: %d %s", to, w.Code, w.Body.String())
		}
	}
	if service.to[0].Session != "child" || service.to[1] != boxCaller().Principal {
		t.Fatalf("targets: %+v", service.to)
	}
}
