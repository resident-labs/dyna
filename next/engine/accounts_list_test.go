package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

func TestAccountList(t *testing.T) {
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
	proxy, err := model.NewProxy(t.TempDir(), "invented-management-key")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"credentials":[{"id":"invented-private-proxy-id","auth_index":"invented-index","provider":"claude","state":"sign_in_expired","models":["invented-model"]},{"id":"custom","auth_index":"custom","provider":"openai-compatible-custom-acme","state":"unknown","display_name":"Acme"}]}`))
	})}
	go func() { _ = server.Serve(proxy.Listener) }()
	defer server.Close()
	actions := NewAccountActions(New(st, nil, nil, time.Now), proxy.Connections("invented-management-key", st, time.Now), proxy.Quotas("invented-management-key", st, time.Now))
	if _, problem := actions.AccountsList(ctx, contract.Principal{Kind: contract.PrincipalSession}, contract.AccountsListRequest{}); problem == nil {
		t.Fatal("session could list accounts")
	}
	response, problem := actions.AccountsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.AccountsListRequest{})
	flows := map[contract.AccountProvider]contract.ConnectFlow{
		contract.ProviderAnthropic: contract.ConnectFlowCallback, contract.ProviderOpenAI: contract.ConnectFlowCallback,
		contract.ProviderKimi: contract.ConnectFlowDevice, contract.ProviderXAI: contract.ConnectFlowDevice, contract.ProviderMeta: contract.ConnectFlowDevice,
		contract.ProviderZAI: contract.ConnectFlowKey, contract.ProviderOpencodeGo: contract.ConnectFlowKey, contract.ProviderDeepSeek: contract.ConnectFlowKey,
		"custom-acme": contract.ConnectFlowKey,
	}
	for _, provider := range response.Providers {
		if provider.ConnectFlow != flows[provider.ID] {
			t.Fatalf("provider %s has wrong connection flow", provider.ID)
		}
		if provider.AlsoKey != (provider.ID == contract.ProviderMeta) {
			t.Fatalf("provider %s has the wrong key option", provider.ID)
		}
	}
	data, err := json.Marshal(response)
	if err != nil || problem != nil || len(response.Accounts) != 1 || len(response.Providers) != 9 || len(response.Accounts[0].Models) != 1 || response.Accounts[0].State != contract.AccountSignInExpired || strings.Contains(string(data), "invented-private-proxy-id") {
		t.Fatal("unsafe or incomplete account list")
	}
}
