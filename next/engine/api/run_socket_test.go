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

	trackRoom contract.RoomID
	trackFor  string
	track     contract.PrTrackRequest

	issueRoom contract.RoomID
	issueFor  string
	issue     contract.IssueTrackRequest
}

func (s *runSocketService) HookTrackIssue(_ context.Context, room contract.RoomID, session string, req contract.IssueTrackRequest) (contract.IssueTrackResponse, *contract.Error) {
	s.issueRoom, s.issueFor, s.issue = room, session, req
	return contract.IssueTrackResponse{}, nil
}

func (s *runSocketService) HookTrack(_ context.Context, room contract.RoomID, session string, req contract.PrTrackRequest) (contract.PrTrackResponse, *contract.Error) {
	s.trackRoom, s.trackFor, s.track = room, session, req
	return contract.PrTrackResponse{}, nil
}

func (s *runSocketService) HookPost(_ context.Context, sub contract.SubscriptionID, room contract.RoomID, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
	s.sub, s.room, s.req = sub, room, req
	return contract.PostResponse{Event: "ev_1", Seq: 1}, nil
}

// A Run's socket fixes its subscription and room, and serves no engine method, whatever the box asks for.
func TestRunSocketServesPostTrackAndGitHubOnly(t *testing.T) {
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
	if got := post("/v1/pr.track", `{"repo":"o/r","number":7,"session":"lead"}`); got != http.StatusOK {
		t.Fatalf("track: %d", got)
	}
	if service.trackRoom != "room_a" || service.trackFor != "lead" || service.track.Repo != "o/r" || service.track.Number != 7 {
		t.Fatalf("track reached the service as %+v", service)
	}
	if got := post("/v1/pr.track", `{"repo":"o/r","number":7,"room":"room_b"}`); got != http.StatusBadRequest {
		t.Fatalf("a Run named another room: %d", got)
	}
	if got := post("/v1/issue.track", `{"repo":"o/r","number":8,"session":"lead"}`); got != http.StatusOK {
		t.Fatalf("issue track: %d", got)
	}
	if service.issueRoom != "room_a" || service.issueFor != "lead" || service.issue.Repo != "o/r" || service.issue.Number != 8 {
		t.Fatalf("issue track reached the service as %+v", service)
	}
	if got := post("/v1/issue.track", `{"repo":"o/r","number":8,"room":"room_b"}`); got != http.StatusBadRequest {
		t.Fatalf("a Run named another room: %d", got)
	}
	for _, method := range []contract.Method{contract.MethodIssueUntrack, contract.MethodIssueList, contract.MethodPrUntrack, contract.MethodRoomList, contract.MethodSessionList, contract.MethodSubscriptionAdd, contract.MethodHostexecRun, contract.MethodSessionStart, contract.MethodEventsList} {
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
