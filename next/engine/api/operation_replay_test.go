package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/store"
)

func TestOperationReplayRespectsDeviceScopes(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	core := engine.New(st, nil, nil, time.Now)
	entry := approve.Entry{
		Scope: contract.ScopeAdmin,
		Class: func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassOwner },
		Card:  func(context.Context, contract.OpArgs, contract.Principal) contract.Card { return contract.Card{} },
		Check: func(context.Context, contract.OpArgs, contract.Principal) error { return nil },
		Execute: func(context.Context, contract.OpID, contract.OpArgs, contract.Principal) contract.OpResult {
			return contract.OpResult{}
		},
		Decode: func(data json.RawMessage) (contract.OpArgs, error) {
			var a contract.HostexecGrantArgs
			err := json.Unmarshal(data, &a)
			return a, err
		},
	}
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": entry, "pr.open": {Scope: contract.ScopeOperate}}, time.Hour); err != nil {
		t.Fatal(err)
	}
	a := New(core, nil)
	admin := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner, Device: "admin"}, Web: true, Scopes: []contract.Scope{contract.ScopeAdmin}}
	limited := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner, Device: "limited"}, Web: true, Scopes: []contract.Scope{contract.ScopeOperate}}
	first := request(a.handler(admin), "/v1/op.submit", `{"clientId":"shared","operation":{"room":"room","kind":"hostexec.grant","args":{"room":"room","to":{"kind":"session","session":"worker"},"hosts":["host"],"reason":"private"}}}`)
	if first.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", first.Code, first.Body.String())
	}
	retry := request(a.handler(limited), "/v1/op.submit", `{"clientId":"shared","operation":{"room":"room","kind":"pr.open","args":{"title":"test","body":"","base":"main","draft":false}}}`)
	if retry.Code != http.StatusForbidden {
		t.Fatalf("limited: %d %s", retry.Code, retry.Body.String())
	}
}
