package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type failingAccountCleanup struct{ *store.Store }

func (f failingAccountCleanup) RemoveAccount(context.Context, contract.AccountID) error {
	return errors.New("invented store failure")
}

// The general endpoint spends account balance instead of Coding Plan quota.
func TestZAIUsesCodingEndpoint(t *testing.T) {
	for _, provider := range KeyProviders() {
		if provider.Provider == contract.ProviderZAI {
			if provider.URL != "https://api.z.ai/api/coding/paas/v4" {
				t.Fatalf("z.ai URL = %s", provider.URL)
			}
			return
		}
	}
	t.Fatal("z.ai is not in the catalog")
}

func TestKeyActivationFailureRollbackIsRecoverable(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dyna/keys":
			var input struct {
				ID string `json:"id"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("invalid key")
				return
			}
			if err := st.SaveProxyAuth(context.Background(), store.ProxyAuth{ID: input.ID, Provider: "zai", Record: []byte("{}")}, true); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte("{}"))
		case "/dyna/credentials/activate":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/dyna/credentials/remove":
			var input struct {
				ID string `json:"id"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("invalid removal")
				return
			}
			if err := st.DeleteProxyAuth(context.Background(), input.ID); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte("{}"))
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("key", failingAccountCleanup{st}, time.Now)
	if result, err := c.SetKey(context.Background(), contract.AccountsSetKeyRequest{Provider: contract.ProviderZAI, Label: "Invented", Key: "invented-key"}); err == nil || result.AccountID != "" {
		t.Fatal("partial rollback reported success")
	}
	accounts, err := st.Accounts(context.Background())
	if err != nil || len(accounts) != 1 {
		t.Fatal("failed cleanup did not retain a removable account")
	}
	if err := st.RemoveAccount(context.Background(), accounts[0].Account.ID); err != nil {
		t.Fatal("retry could not remove the account")
	}
}

func TestKeyActivationFailureRollsBack(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dyna/keys":
			var input struct {
				ID string `json:"id"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("invalid request")
				return
			}
			if err := st.SaveProxyAuth(context.Background(), store.ProxyAuth{ID: input.ID, Provider: "zai", Record: []byte("{}")}, true); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte("{}"))
		case "/dyna/credentials/activate":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/dyna/credentials/remove":
			var input struct {
				ID string `json:"id"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("invalid removal")
				return
			}
			if err := st.DeleteProxyAuth(context.Background(), input.ID); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte("{}"))
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("key", st, time.Now)
	if _, err := c.SetKey(context.Background(), contract.AccountsSetKeyRequest{Provider: contract.ProviderZAI, Label: "Invented", Key: "invented-key"}); err == nil {
		t.Fatal("activation failure reported success")
	}
	accounts, err := st.Accounts(context.Background())
	if err != nil || len(accounts) != 0 {
		t.Fatal("failed activation left an account")
	}
	auths, err := st.ListProxyAuths(context.Background())
	if err != nil || len(auths) != 0 {
		t.Fatal("failed activation left a credential")
	}
}

func TestKeyProvidersBindOnlyDurableCredentials(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	var lastID string
	activated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer invented-management-key" {
			t.Error("missing proxy authentication")
		}
		if r.URL.Path == "/dyna/credentials/activate" {
			var input struct {
				Model string `json:"model"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("invalid activation")
			}
			activated = activated || input.Model == MetaKeyModel
			_, _ = w.Write([]byte("{}"))
			return
		}
		var input struct {
			ID       string                   `json:"id"`
			Provider contract.AccountProvider `json:"provider"`
			Key      string                   `json:"key"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Key != "invented-key" {
			t.Error("missing invented key")
		}
		lastID = input.ID
		if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: input.ID, Provider: string(input.Provider), Record: []byte("{}")}, true); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{url: server.URL, base: http.DefaultTransport}}}
	c := p.Connections("invented-management-key", st, time.Now)
	providers := []contract.AccountProvider{contract.ProviderMeta}
	for _, provider := range KeyProviders() {
		providers = append(providers, provider.Provider)
	}
	for _, provider := range providers {
		response, err := c.SetKey(ctx, contract.AccountsSetKeyRequest{Provider: provider, Label: "Invented", Key: "invented-key"})
		if err != nil || response.AccountID == "" || string(response.AccountID) == lastID {
			t.Fatal("key was not bound privately")
		}
	}
	if !activated {
		t.Fatal("no Meta key activation named its model")
	}
}
