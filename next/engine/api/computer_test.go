package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type computerService struct {
	*fakeService
	problem *contract.Error
	input   *contract.ComputerInputRequest
}

func (s computerService) ComputerWatch(context.Context, contract.SessionID) (<-chan []byte, *contract.Error) {
	if s.problem != nil {
		return nil, s.problem
	}
	ch := make(chan []byte, 1)
	ch <- []byte("jpeg")
	close(ch)
	return ch, nil
}
func (s computerService) ComputerInput(_ context.Context, _ contract.Principal, req contract.ComputerInputRequest) (contract.ComputerInputResponse, *contract.Error) {
	*s.input = req
	return contract.ComputerInputResponse{}, nil
}

func TestComputerInputIsOwnerOnlyAndValidated(t *testing.T) {
	const key = `{"session":"s","frame":{"width":1280,"height":800},"events":[{"kind":"key.down","key":"a","x":0,"y":0}]}`
	owner := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeComputers}}
	for _, tc := range []struct {
		name   string
		caller Caller
		body   string
		status int
	}{
		{"valid key", owner, key, http.StatusOK},
		{"session", boxCaller(), key, http.StatusForbidden},
		{"device without scope", Caller{Principal: owner.Principal, Web: true, Scopes: []contract.Scope{contract.ScopeRead}}, key, http.StatusForbidden},
		{"bad kind", owner, strings.Replace(key, "key.down", "key.sideways", 1), http.StatusBadRequest},
		{"no frame", owner, strings.Replace(key, `"width":1280`, `"width":0`, 1), http.StatusBadRequest},
		{"click without button", owner, strings.Replace(key, `"kind":"key.down","key":"a"`, `"kind":"mouse.down"`, 1), http.StatusBadRequest},
		{"outside frame", owner, strings.Replace(key, `"x":0`, `"x":1281`, 1), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := &contract.ComputerInputRequest{}
			s := computerService{fakeService: &fakeService{}, input: got}
			w := request(New(s, nil).handler(tc.caller), "/v1/computer.input", tc.body)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if (got.Session != "") != (tc.status == http.StatusOK) {
				t.Fatalf("service saw %+v", got)
			}
			if tc.status == http.StatusOK && got.Events[0].Kind != contract.InputKeyDown {
				t.Fatalf("service saw %+v", got)
			}
		})
	}
}

func TestComputerWatchResponseAndErrorsAreNotCached(t *testing.T) {
	for _, tc := range []struct {
		problem *contract.Error
		status  int
	}{
		{nil, http.StatusOK}, {&contract.Error{Code: contract.ErrNotFound}, http.StatusNotFound}, {&contract.Error{Code: contract.ErrConflict}, http.StatusConflict},
		{&contract.Error{Code: contract.ErrNoGrant}, http.StatusForbidden}, {&contract.Error{Code: contract.ErrMoveBusy}, http.StatusConflict},
		{&contract.Error{Code: contract.ErrHasChildren}, http.StatusConflict}, {&contract.Error{Code: contract.ErrParentClosing}, http.StatusConflict},
	} {
		s := computerService{fakeService: &fakeService{}, problem: tc.problem}
		h := New(s, nil).handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeComputers}})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/computers/watch?session=s", nil))
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status %d headers %v", w.Code, w.Header())
		}
		if tc.problem == nil && (!strings.Contains(w.Header().Get("Content-Type"), "boundary=frame") || !strings.Contains(w.Body.String(), "image/jpeg") || !strings.Contains(w.Body.String(), "jpeg")) {
			t.Fatalf("frame response: %s", w.Body.String())
		}
	}
}
