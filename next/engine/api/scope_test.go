package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestWebScopeRefusals(t *testing.T) {
	a := New(&fakeService{}, nil)
	a.SetFilesRoute(func(Caller) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})
	c := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true}
	for _, tc := range []struct{ path, method, body string }{
		{"/v1/room.list", "POST", `{}`},
		{"/v1/post", "POST", `{}`},
		{"/v1/host.add", "POST", `{}`},
		{"/v1/files", "GET", ""},
		{"/v1/terminal", "GET", ""},
		{"/v1/computers/watch", "GET", ""},
		{"/v1/events", "GET", ""},
		{"/v1/shell/watch", "GET", ""},
		{"/v1/op.submit", "POST", `{"operation":{"kind":"hostexec.grant","args":{"room":"r"}}}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			a.handler(c).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if w.Code != http.StatusForbidden {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
