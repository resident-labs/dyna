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

var testKeyProviders = []KeyProvider{{Provider: "zai", DisplayName: "z.ai", KeyName: "ZAI_API_KEY", URL: "https://example.com/v1", Models: []contract.CatalogModel{{ID: "glm-test", Name: "GLM"}}}}

type failingAccountCleanup struct{ *store.Store }

func (f failingAccountCleanup) RemoveAccount(context.Context, contract.AccountID) error {
	return errors.New("invented store failure")
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
	p := &Proxy{keyProviders: testKeyProviders, client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("key", failingAccountCleanup{st}, time.Now)
	if result, err := c.SetKey(context.Background(), contract.AccountsSetKeyRequest{Provider: contract.AccountProvider("zai"), Label: "Invented", Key: "invented-key"}); err == nil || result.AccountID != "" {
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
	p := &Proxy{keyProviders: testKeyProviders, client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("key", st, time.Now)
	if _, err := c.SetKey(context.Background(), contract.AccountsSetKeyRequest{Provider: contract.AccountProvider("zai"), Label: "Invented", Key: "invented-key"}); err == nil {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer invented-management-key" {
			t.Error("missing proxy authentication")
		}
		if r.URL.Path == "/dyna/credentials/activate" {
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
	p := &Proxy{keyProviders: testKeyProviders, client: &http.Client{Transport: quotaTestTransport{url: server.URL, base: http.DefaultTransport}}}
	c := p.Connections("invented-management-key", st, time.Now)
	for _, provider := range testKeyProviders {
		response, err := c.SetKey(ctx, contract.AccountsSetKeyRequest{Provider: provider.Provider, Label: "Invented", Key: "invented-key"})
		if err != nil || response.AccountID == "" || string(response.AccountID) == lastID {
			t.Fatal("key was not bound privately")
		}
	}
}
