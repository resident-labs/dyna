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

func testAccountCatalog() model.Catalog {
	return model.Catalog{Plans: []model.PlanProvider{
		{ID: "anthropic", Models: []model.PlanModel{{ID: "claude-sonnet-4-6"}}},
		{ID: "openai", Models: []model.PlanModel{{ID: "gpt-5.6-luna"}}},
		{ID: "kimi-coding", Models: []model.PlanModel{{ID: "k3", ProxyID: "kimi-k3"}}},
		{ID: "xai", Models: []model.PlanModel{{ID: "grok-4.3"}}},
	}, Keys: []model.KeyProvider{{Provider: "zai", DisplayName: "z.ai", KeyName: "ZAI_API_KEY", DefaultModel: "model", URL: "https://example.com/v1", Models: []contract.CatalogModel{{ID: "model"}}}}}
}

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
	proxy, err := model.NewProxy(t.TempDir(), "invented-management-key", testAccountCatalog())
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"credentials":[{"id":"invented-private-proxy-id","auth_index":"invented-index","provider":"claude","state":"sign_in_expired"}]}`))
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
		contract.ProviderKimiCoding: contract.ConnectFlowDevice, contract.ProviderXAI: contract.ConnectFlowDevice,
		contract.AccountProvider("zai"): contract.ConnectFlowKey,
	}
	for _, provider := range response.Providers {
		if len(provider.Flows) == 0 || provider.Flows[0] != flows[provider.ID] {
			t.Fatalf("provider %s has wrong connection flow", provider.ID)
		}
	}
	data, err := json.Marshal(response)
	if err != nil || problem != nil || len(response.Accounts) != 1 || len(response.Providers) != 5 || response.Accounts[0].Flow != contract.ConnectFlowCallback || response.Accounts[0].State != contract.AccountSignInExpired || strings.Contains(string(data), "invented-private-proxy-id") {
		t.Fatal("unsafe or incomplete account list")
	}
}
