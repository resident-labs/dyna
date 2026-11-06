package api

import (
	"github.com/resident-labs/dyna/contract"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSharedRouteNeedsUserReadScope(t *testing.T) {
	a := New(&fakeService{}, nil)
	a.SetSharedRoute(func(Caller) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("room") != "test" {
				t.Fatal(r.PathValue("room"))
			}
			w.WriteHeader(200)
		})
	})
	for _, tc := range []struct {
		caller Caller
		want   int
	}{
		{Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeRead}}, 200},
		{Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeOperate}}, 403},
		{boxCaller(), 403},
	} {
		for _, kind := range []string{"tree", "file"} {
			rec := httptest.NewRecorder()
			a.handler(tc.caller).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/rooms/test/shared/"+kind, nil))
			if rec.Code != tc.want {
				t.Fatalf("%s: got %d want %d", kind, rec.Code, tc.want)
			}
		}
	}
}
