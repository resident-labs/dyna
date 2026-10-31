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
