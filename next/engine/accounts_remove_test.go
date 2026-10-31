package engine

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

func TestAccountRemoveRetriesAfterCredentialDeletion(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: "invented", Provider: "claude", Record: []byte("{}")}, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", "invented", contract.ProviderAnthropic, "Invented"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProxyAuth(ctx, "invented"); err != nil {
		t.Fatal(err)
	} // Simulate a crash before the row was deleted.
	proxy, err := model.NewProxy(t.TempDir(), "invented-key")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{}")) })}
	go func() { _ = server.Serve(proxy.Listener) }()
	defer server.Close()
	core := New(st, nil, nil, time.Now)
	actions := NewAccountActions(core, proxy.Connections("invented-key", st, time.Now))
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, problem := actions.AccountsRemove(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.AccountsRemoveRequest{AccountID: "account"}); problem != nil {
		t.Fatal("retry failed")
	}
	if _, err := st.AccountProxyID(ctx, "account"); err == nil {
		t.Fatal("retry left the account row")
	}
}
