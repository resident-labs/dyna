package api

import (
	"context"
	"github.com/resident-labs/dyna/engine/githubwrites"
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

type githubWritesService struct {
	githubToolsService
	byRoom map[contract.RoomID]contract.GitHubWrites
}

func (s *githubWritesService) GitHubWritesOf(_ context.Context, room contract.RoomID) (contract.GitHubWrites, error) {
	return s.byRoom[room], nil
}

// The writes a box's request may make are those of the room its socket was opened for. A room named in the request's headers,
// query or body changes nothing, and a room with none gets none.
func TestGitHubToolsGetTheSocketsRoomWrites(t *testing.T) {
	var seen []contract.GitHubWrites
	service := &githubWritesService{byRoom: map[contract.RoomID]contract.GitHubWrites{
		"room_a": {Repos: []contract.RepoName{"acme/app"}, Groups: []contract.GitHubWriteGroup{contract.GitHubWriteIssues}},
	}}
	service.tools = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, githubwrites.AllowedFrom(r.Context()))
		if caller := githubwrites.SessionFrom(r.Context()); caller != "ses" {
			t.Errorf("socket caller = %q", caller)
		}
	})
	call := func(room contract.RoomID) {
		server := httptest.NewServer(New(service, nil).handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalSession, Session: "ses"}, Box: "box", Room: room}))
		defer server.Close()
		request, _ := http.NewRequest("POST", server.URL+"/github/?room=room_a", strings.NewReader(`{"room":"room_a","session":"other"}`))
		request.Header.Set("X-Dyna-Room", "room_a")
		request.Header.Set("X-Dyna-Session", "other")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	call("room_a")
	call("room_b")
	if len(seen) != 2 || !seen[0].On() || !seen[0].Allows("acme/app") || seen[1].On() {
		t.Fatalf("grants seen: %+v", seen)
	}
}
