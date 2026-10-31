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
		{"/v1/op.submit", "POST", `{"operation":{"kind":"pr.open","args":{"title":"t","body":"b","base":"main","draft":false}}}`},
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

func TestShellWatchNeedsTerminalScope(t *testing.T) {
	a := New(&fakeService{}, nil)
	c := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeRead}}
	w := httptest.NewRecorder()
	a.handler(c).ServeHTTP(w, httptest.NewRequest("GET", "/v1/shell/watch?session=session-a", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}

func TestFileRouteScopes(t *testing.T) {
	a := New(&fakeService{}, nil)
	a.SetFilesRoute(func(Caller) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})
	for _, tc := range []struct {
		name         string
		scopes       []contract.Scope
		method, path string
		want         int
	}{
		{"read get", []contract.Scope{contract.ScopeRead}, "GET", "/v1/files/hash", 200},
		{"read head", []contract.Scope{contract.ScopeRead}, "HEAD", "/v1/files/hash", 200},
		{"read post", []contract.Scope{contract.ScopeRead}, "POST", "/v1/files", 403},
		{"read operate get", []contract.Scope{contract.ScopeRead, contract.ScopeOperate}, "GET", "/v1/files/hash", 200},
		{"read operate post", []contract.Scope{contract.ScopeRead, contract.ScopeOperate}, "POST", "/v1/files", 200},
		{"no read get", []contract.Scope{contract.ScopeOperate}, "GET", "/v1/files/hash", 403},
		{"other method no files", []contract.Scope{contract.ScopeRead, contract.ScopeOperate}, "DELETE", "/v1/files/hash", 403},
		{"other method files", []contract.Scope{contract.ScopeFiles}, "DELETE", "/v1/files/hash", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: tc.scopes}
			w := httptest.NewRecorder()
			a.handler(c).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func (f *fakeService) OperationScope(kind contract.OpKind) contract.Scope {
	if kind == "pr.open" {
		return contract.ScopeOperate
	}
	return ""
}

func TestDeviceManagementRequiresAdminOwner(t *testing.T) {
	a := New(&fakeService{}, nil)
	for _, c := range []Caller{
		{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeOperate}},
		boxCaller(),
	} {
		for _, method := range []contract.Method{contract.MethodDevicePair, contract.MethodDeviceList, contract.MethodDeviceRevoke, contract.MethodDeviceSetScopes, contract.MethodConnectStart, contract.MethodConnectStatus, contract.MethodConnectCancel, contract.MethodConnectUnlink} {
			w := request(a.handler(c), "/v1/"+string(method), `{}`)
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s: got %d", method, w.Code)
			}
		}
	}
}

func TestDeviceScopesReturnsTheCallersOwn(t *testing.T) {
	a := New(&fakeService{}, nil)
	for _, tc := range []struct {
		name   string
		caller Caller
		want   string
	}{
		{"read device", Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeRead}}, `{"scopes":["read"]}`},
		{"device without read", Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeTerminal}}, `{"scopes":["terminal"]}`},
		{"owner socket", Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}, `{"scopes":["read","operate","files","terminal","computers","admin"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(a.handler(tc.caller), "/v1/device.scopes", `{}`)
			if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != tc.want {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
}
