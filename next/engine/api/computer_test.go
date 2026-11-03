package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/resident-labs/dyna/contract"
)

type computerService struct {
	*fakeService
	argv    []string
	problem *contract.Error
	started *contract.ComputerStartRequest
	closed  chan struct{}
}

func (s computerService) ComputerStart(_ context.Context, _ contract.Principal, req contract.ComputerStartRequest) (contract.ComputerStartResponse, *contract.Error) {
	*s.started = req
	return contract.ComputerStartResponse{}, nil
}

func (s computerService) ComputerStream(context.Context, contract.SessionID) ([]string, func(), *contract.Error) {
	return s.argv, func() { close(s.closed) }, s.problem
}

func TestComputerStartIsOwnerOnlyAndNeedsComputers(t *testing.T) {
	owner := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeComputers}}
	for _, tc := range []struct {
		name   string
		caller Caller
		body   string
		status int
	}{
		{"owner", owner, `{"session":"s"}`, http.StatusOK},
		{"session", boxCaller(), `{"session":"s"}`, http.StatusForbidden},
		{"device without scope", Caller{Principal: owner.Principal, Web: true, Scopes: []contract.Scope{contract.ScopeRead}}, `{"session":"s"}`, http.StatusForbidden},
		{"no session", owner, `{}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := &contract.ComputerStartRequest{}
			s := computerService{fakeService: &fakeService{}, started: got}
			w := request(New(s, nil).handler(tc.caller), "/v1/computer.start", tc.body)
			if w.Code != tc.status || (got.Session == "s") != (tc.status == http.StatusOK) {
				t.Fatalf("got %d, service saw %+v: %s", w.Code, got, w.Body.String())
			}
		})
	}
}

// The stream passes bytes both ways as they are, and closing the view ends the command and lets the box sleep.
func TestComputerStreamCarriesBytesAndReleasesTheBox(t *testing.T) {
	s := computerService{fakeService: &fakeService{}, argv: []string{"cat"}, closed: make(chan struct{})}
	a := New(s, nil)
	w := httptest.NewRecorder()
	a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeRead}}).
		ServeHTTP(w, httptest.NewRequest("GET", "/v1/computers/stream?session=s", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("device without scope: %d", w.Code)
	}
	w = httptest.NewRecorder()
	a.handler(boxCaller()).ServeHTTP(w, httptest.NewRequest("GET", "/v1/computers/stream?session=s", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("session: %d", w.Code)
	}
	server := httptest.NewServer(a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}))
	defer server.Close()
	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/computers/stream?session=s", nil)
	if err != nil {
		t.Fatal(err)
	}
	sent := []byte{'R', 'F', 'B', 0, 1, 0xff, '\n', 3}
	if err := conn.Write(ctx, websocket.MessageBinary, sent); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for len(got) < len(sent) {
		kind, data, err := conn.Read(ctx)
		if err != nil || kind != websocket.MessageBinary {
			t.Fatalf("echo %q: %v", data, err)
		}
		got = append(got, data...)
	}
	if !bytes.Equal(got, sent) {
		t.Fatalf("echo %q", got)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	select {
	case <-s.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the closed view still holds the box")
	}
}

func TestComputerStreamErrors(t *testing.T) {
	s := computerService{fakeService: &fakeService{}, problem: &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}}
	w := httptest.NewRecorder()
	New(s, nil).handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}).ServeHTTP(w, httptest.NewRequest("GET", "/v1/computers/stream?session=s", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d", w.Code)
	}
	w = httptest.NewRecorder()
	New(s, nil).handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}).ServeHTTP(w, httptest.NewRequest("GET", "/v1/computers/stream", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("no session: %d", w.Code)
	}
}
