package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestAccountsListIsOwnerOnlyAndOmitsProxyIdentity(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: "invented-private-proxy-id", Provider: "claude", Record: []byte("{}")}, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", "invented-private-proxy-id", contract.ProviderAnthropic, "Invented"); err != nil {
		t.Fatal(err)
	}
	actions := NewAccountActions(New(st, nil, nil, time.Now), nil)
	if _, problem := actions.AccountsList(ctx, contract.Principal{Kind: contract.PrincipalSession}, contract.AccountsListRequest{}); problem == nil {
		t.Fatal("session could list accounts")
	}
	response, problem := actions.AccountsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.AccountsListRequest{})
	data, err := json.Marshal(response)
	if err != nil || problem != nil || len(response.Accounts) != 1 || len(response.Providers) != 7 || strings.Contains(string(data), "invented-private-proxy-id") {
		t.Fatal("unsafe or incomplete account list")
	}
}
