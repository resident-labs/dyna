package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubwrites"
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
	for _, method := range []string{"post", "session.start", "goal.set", "hostexec.run", "question.ask", "session.close", "discord.send"} {
		got := request(New(advisorService{advisor: true}, nil).handler(boxCaller()), "/v1/"+method, `{"text":"hi","room":"r"}`)
		if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), contract.AdvisorRefusal) {
			t.Fatalf("%s by the advisor: %d %s", method, got.Code, got.Body)
		}
	}
	if got := request(New(advisorService{}, nil).handler(boxCaller()), "/v1/post", `{"text":"hi","room":"r"}`); got.Code != http.StatusOK {
		t.Fatalf("post by another session: %d %s", got.Code, got.Body)
	}
}

func TestTheAdvisorKeepsTheReadsEverySessionNeeds(t *testing.T) {
	for _, method := range []contract.Method{contract.MethodBoxHello, contract.MethodTurnOutput, contract.MethodTranscriptAppend, contract.MethodShellOutput, contract.MethodContextSnapshot, contract.MethodSessionList} {
		if !advisorReads[method] {
			t.Fatalf("the advisor may not call %s", method)
		}
	}
}

type grantedAdvisorService struct {
	advisorService
	tools http.Handler
}

func (s grantedAdvisorService) GitHubTools() http.Handler { return s.tools }
func (grantedAdvisorService) GitHubWritesOf(context.Context, contract.RoomID) (contract.GitHubWrites, error) {
	return contract.GitHubWrites{Repos: []contract.RepoName{"a/b"}, Groups: []contract.GitHubWriteGroup{"issues"}}, nil
}

func TestTheAdvisorGetsNoGitHubWriteGrant(t *testing.T) {
	for _, advisor := range []bool{true, false} {
		var got contract.GitHubWrites
		tools := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = githubwrites.AllowedFrom(r.Context()) })
		request(New(grantedAdvisorService{advisorService{advisor: advisor}, tools}, nil).handler(boxCaller()), "/github/mcp", `{}`)
		if got.On() == advisor {
			t.Fatalf("advisor=%v: write grant on is %v", advisor, got.On())
		}
	}
}
