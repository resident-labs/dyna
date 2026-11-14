package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type advisorService struct {
	Service
	advisor bool
}

func (a advisorService) IsAdvisor(context.Context, contract.Principal) bool { return a.advisor }

func (advisorService) Post(context.Context, contract.Principal, contract.PostRequest) (contract.PostResponse, *contract.Error) {
	return contract.PostResponse{}, nil
}

func TestTheAdvisorMeetsOneRefusalWhereItActs(t *testing.T) {
	for _, method := range []string{"post", "session.start", "goal.set", "hostexec.run", "discord.send"} {
		got := request(New(advisorService{advisor: true}, nil).handler(boxCaller()), "/v1/"+method, `{"text":"hi","room":"r"}`)
		if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), contract.AdvisorRefusal) {
			t.Fatalf("%s by the advisor: %d %s", method, got.Code, got.Body)
		}
	}
	if got := request(New(advisorService{}, nil).handler(boxCaller()), "/v1/post", `{"text":"hi","room":"r"}`); got.Code != http.StatusOK {
		t.Fatalf("post by another session: %d %s", got.Code, got.Body)
	}
}
