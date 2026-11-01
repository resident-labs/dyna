package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type runSocketService struct {
	fakeService
	sub  contract.SubscriptionID
	room contract.RoomID
	req  contract.PostRequest
}

func (s *runSocketService) HookPost(_ context.Context, sub contract.SubscriptionID, room contract.RoomID, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
	s.sub, s.room, s.req = sub, room, req
	return contract.PostResponse{Event: "ev_1", Seq: 1}, nil
}

// A Run's socket fixes its subscription and room, and serves no engine method, whatever the box asks for.
func TestRunSocketServesPostAndGitHubOnly(t *testing.T) {
	service := &runSocketService{}
	a := New(service, nil)
	var github []string
	a.SetGitHubRead(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { github = append(github, r.Method+" "+r.URL.Path) }))
	server := httptest.NewServer(a.runHandler("sub_a", "room_a"))
	defer server.Close()
	post := func(path, body string) int {
		t.Helper()
		resp, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if got := post("/v1/post", `{"text":"hello","mentions":["lead"]}`); got != http.StatusOK {
		t.Fatalf("post: %d", got)
	}
	if service.sub != "sub_a" || service.room != "room_a" || service.req.Text != "hello" {
		t.Fatalf("post reached the service as %+v", service)
	}
	if got := post("/v1/post", `{"text":"hello","surprise":1}`); got != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", got)
	}
	for _, method := range []contract.Method{contract.MethodRoomList, contract.MethodSessionList, contract.MethodSubscriptionAdd, contract.MethodHostexecRun, contract.MethodSessionStart, contract.MethodEventsList} {
		if got := post("/v1/"+string(method), `{}`); got != http.StatusForbidden {
			t.Errorf("%s: %d", method, got)
		}
	}
	if len(github) != 0 {
		t.Fatalf("an engine path reached GitHub: %v", github)
	}
	resp, err := http.Get(server.URL + "/repos/o/r/issues")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(github) != 1 || github[0] != "GET /repos/o/r/issues" {
		t.Fatalf("GitHub route: %v", github)
	}
}
