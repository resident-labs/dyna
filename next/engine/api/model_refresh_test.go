package api

import (
	"net/http"
	"testing"
)

func TestModelRouteUsesCurrentProfile(t *testing.T) {
	allowed := "first"
	a := New(&fakeService{}, func(Caller) http.Handler {
		snapshot := allowed
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("model") != snapshot {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	})
	h := a.handler(boxCaller())
	if got := request(h, "/model/?model=first", "{}"); got.Code != http.StatusOK {
		t.Fatalf("first request: %d", got.Code)
	}
	allowed = "second"
	if got := request(h, "/model/?model=second", "{}"); got.Code != http.StatusOK {
		t.Fatalf("after profile change: %d", got.Code)
	}
}
