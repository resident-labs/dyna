package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type proxyKeyService struct{ fakeService }

func (*proxyKeyService) ProxyKey(context.Context, contract.Principal, contract.ProxyKeyRequest) (contract.ProxyKeyResponse, *contract.Error) {
	return contract.ProxyKeyResponse{Key: "owner-key"}, nil
}

// The route and its key answer only on the owner socket: never a box, and never the
// web listener, which paired devices and Connect reach.
func TestLocalModelRouteIsOwnerSocketOnly(t *testing.T) {
	a := New(&proxyKeyService{}, func(Caller) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	})
	a.SetLocalModelRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	owner := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}
	web := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeAdmin, contract.ScopeRead}}
	for name, c := range map[string]struct {
		caller Caller
		route  int
		method int
	}{
		"owner socket": {owner, http.StatusAccepted, http.StatusOK},
		"box socket":   {boxCaller(), http.StatusTeapot, http.StatusForbidden},
		"web listener": {web, http.StatusNotFound, http.StatusForbidden},
	} {
		h := a.handler(c.caller)
		if got := request(h, "/model/v1/messages", `{"model":"any"}`).Code; got != c.route {
			t.Errorf("%s: route status %d", name, got)
		}
		if got := request(h, "/v1/proxy.key", `{}`).Code; got != c.method {
			t.Errorf("%s: proxy.key status %d", name, got)
		}
	}
	if got := request(a.runHandler("sub", "room"), "/model/v1/messages", `{"model":"any"}`).Code; got == http.StatusAccepted {
		t.Error("a Run's socket reached the owner's route")
	}
}
