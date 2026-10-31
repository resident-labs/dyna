package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

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
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{url: server.URL, base: http.DefaultTransport}}}
	c := p.Connections("invented-management-key", st, time.Now)
	for _, provider := range KeyProviders() {
		response, err := c.SetKey(ctx, contract.AccountsSetKeyRequest{Provider: provider.Provider, Label: "Invented", Key: "invented-key"})
		if err != nil || response.AccountID == "" || string(response.AccountID) == lastID {
			t.Fatal("key was not bound privately")
		}
	}
}
