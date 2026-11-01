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

func TestImportOAuthStagesBindsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dyna/oauth/import":
			var in struct {
				ID         string    `json:"id"`
				Provider   string    `json:"provider"`
				Credential string    `json:"credential"`
				ExpiresAt  time.Time `json:"expiresAt"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Credential != `{"access_token":"access","refresh_token":"refresh"}` || !in.ExpiresAt.Equal(time.Unix(2000000000, 0)) {
				t.Error("credential was not forwarded")
				return
			}
			if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: in.ID, Provider: in.Provider, Record: []byte("{}")}, true); err != nil {
				t.Error(err)
			}
		case "/dyna/credentials/activate":
			var in struct {
				ID string `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			bindings, err := st.Accounts(ctx)
			found := false
			for _, binding := range bindings {
				if binding.ProxyID == in.ID {
					found = true
				}
			}
			if err != nil || !found {
				t.Error("activation preceded binding")
			}
			if fail {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		case "/dyna/credentials/remove":
			var in struct {
				ID string `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if err := st.DeleteProxyAuth(ctx, in.ID); err != nil {
				t.Error(err)
			}
		default:
			t.Error("unexpected request")
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("key", st, time.Now)
	req := contract.AccountsImportOAuthRequest{Provider: contract.ProviderKimi, Label: "Example", ExpiresAt: time.Unix(2000000000, 0), Credential: `{"access_token":"access","refresh_token":"refresh"}`}
	if out, err := c.ImportOAuth(ctx, req); err != nil || out.AccountID == "" {
		t.Fatal("import failed")
	}
	fail = true
	req.Provider = contract.ProviderXAI
	if _, err := c.ImportOAuth(ctx, req); err == nil {
		t.Fatal("failed activation reported success")
	}
	bindings, err := st.Accounts(ctx)
	if err != nil || len(bindings) != 1 {
		t.Fatal("rollback left an account")
	}
	records, err := st.ListProxyAuths(ctx)
	if err != nil || len(records) != 1 {
		t.Fatal("rollback left a credential")
	}
}
