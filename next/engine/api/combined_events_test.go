package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type combinedService struct {
	Service
	after, roomAfter int64
	called           bool
}

func (s *combinedService) CombinedEvents(_ context.Context, _ contract.Principal, room contract.RoomID, a, b int64) (<-chan contract.Event, *contract.Error) {
	s.called = true
	s.after = a
	s.roomAfter = b
	ch := make(chan contract.Event)
	close(ch)
	return ch, nil
}
func (s *combinedService) Events(context.Context, contract.Principal, contract.RoomID, int64) (<-chan contract.Event, *contract.Error) {
	ch := make(chan contract.Event)
	close(ch)
	return ch, nil
}

func TestStreamHelloRoomUnionCapability(t *testing.T) {
	for _, tc := range []struct {
		query      string
		kind       contract.PrincipalKind
		advertised bool
	}{
		{"", contract.PrincipalOwner, true},
		{"room=r", contract.PrincipalOwner, true},
		{"room=r&roomAfter=0", contract.PrincipalOwner, true},
		{"room=r", contract.PrincipalSession, false},
	} {
		t.Run(tc.query+string(tc.kind), func(t *testing.T) {
			c := Caller{Principal: contract.Principal{Kind: tc.kind}, Room: "r", Web: true, Scopes: []contract.Scope{contract.ScopeRead}}
			w := httptest.NewRecorder()
			New(&combinedService{}, nil).handler(c).ServeHTTP(w, httptest.NewRequest("GET", "/v1/events?"+tc.query, nil))
			if w.Code != 200 || strings.Contains(w.Body.String(), `"roomUnion":true`) != tc.advertised {
				t.Fatalf("status %d hello %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCombinedEventsRoute(t *testing.T) {
	for _, tc := range []struct {
		query, last string
		principal   contract.PrincipalKind
		scope       bool
		status      int
		a, b        int64
	}{
		{"room=r&after=100&roomAfter=2", "50", contract.PrincipalOwner, true, 200, 100, 50},
		{"room=r&after=2&roomAfter=100", "50", contract.PrincipalOwner, true, 200, 50, 100},
		{"room=r&after=2&roomAfter=3", "", contract.PrincipalOwner, true, 200, 2, 3},
		{"room=r&roomAfter=0", "", contract.PrincipalSession, true, 403, 0, 0},
		{"room=r&roomAfter=0", "", contract.PrincipalOwner, false, 403, 0, 0},
		{"roomAfter=0", "", contract.PrincipalOwner, true, 400, 0, 0},
		{"room=r&roomAfter=", "", contract.PrincipalOwner, true, 400, 0, 0},
		{"room=r&roomAfter=-1", "", contract.PrincipalOwner, true, 400, 0, 0},
		{"room=r&roomAfter=0", "bad", contract.PrincipalOwner, true, 400, 0, 0},
	} {
		t.Run(tc.query+tc.last+string(tc.principal), func(t *testing.T) {
			s := &combinedService{}
			c := Caller{Principal: contract.Principal{Kind: tc.principal}, Room: "r", Web: true}
			if tc.scope {
				c.Scopes = []contract.Scope{contract.ScopeRead}
			}
			r := httptest.NewRequest("GET", "/v1/events?"+tc.query, nil)
			r.Header.Set("Last-Event-ID", tc.last)
			w := httptest.NewRecorder()
			New(s, nil).handler(c).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if s.called != (tc.status == 200) || s.called && (s.after != tc.a || s.roomAfter != tc.b) {
				t.Fatalf("called %v bounds %d/%d", s.called, s.after, s.roomAfter)
			}
		})
	}
}
