package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type githubToolsService struct {
	fakeService
	tools http.Handler
}

func (s *githubToolsService) GitHubTools() http.Handler { return s.tools }

// A box socket passes /github/ to the service's GitHub tools. Without them it says why, and the owner's socket has no such route.
func TestBoxSocketServesGitHubTools(t *testing.T) {
	var reached []string
	service := &githubToolsService{tools: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusAccepted)
	})}
	boxCaller := Caller{Principal: contract.Principal{Kind: contract.PrincipalSession, Session: "ses_a"}, Box: "box_a", Room: "room_a"}
	get := func(a *API, c Caller) (int, string) {
		t.Helper()
		server := httptest.NewServer(a.handler(c))
		defer server.Close()
		response, err := http.Post(server.URL+"/github/", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}
	if status, _ := get(New(service, nil), boxCaller); status != http.StatusAccepted || len(reached) != 1 || reached[0] != "POST /github/" {
		t.Fatalf("box: %d %v", status, reached)
	}
	if status, body := get(New(service, nil), Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}); status != http.StatusNotFound || len(reached) != 1 {
		t.Fatalf("owner: %d %s", status, body)
	}
	service.tools = nil
	if status, body := get(New(service, nil), boxCaller); status != http.StatusServiceUnavailable || !strings.Contains(body, "no GitHub token") {
		t.Fatalf("no tools: %d %s", status, body)
	}
	if status, body := get(New(&fakeService{}, nil), boxCaller); status != http.StatusServiceUnavailable || !strings.Contains(body, "no GitHub token") {
		t.Fatalf("a service without tools: %d %s", status, body)
	}
}
