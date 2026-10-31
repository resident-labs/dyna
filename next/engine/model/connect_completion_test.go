package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestExpiredCompletionRejectsWithoutBreakingOtherAttempts(t *testing.T) {
	now := time.Unix(1000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url":"https://provider.example/login","state":"invented"}`))
	}))
	defer server.Close()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("key", nil, func() time.Time { return now })
	start, err := c.Start(context.Background(), contract.AccountsConnectRequest{Provider: contract.ProviderKimiCoding})
	if err != nil {
		t.Fatal(err)
	}
	now = start.ExpiresAt
	rpc := AuthRPC{Complete: func(ctx context.Context, done OAuthCompletion) error {
		return c.Complete(ctx, done, func(fn func() error) error { return fn() })
	}}
	var result OAuthCompletionResult
	if err := rpc.OAuthCompleted(OAuthCompletion{State: "invented", AuthID: "invented-id"}, &result); err != nil || result.Accepted {
		t.Fatal("expired completion was not safely rejected")
	}
	status, err := c.Status(context.Background(), contract.AccountsConnectStatusRequest{Attempt: start.Attempt}, func(fn func() error) error { return fn() })
	if err != nil || status.Status != contract.ConnectFailed {
		t.Fatal("rejected attempt remained bindable")
	}
}

func TestConnectCompletionWithoutBrowserPolling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v8/management/oauth/auth-url":
			_, _ = w.Write([]byte(`{"url":"https://provider.example/login","state":"invented-state"}`))
		case "/v8/management/oauth/status":
			_, _ = w.Write([]byte(`{"status":"ok","auth_id":"invented-id","saved":true,"adopted":true}`))
		default:
			t.Error("unexpected proxy request")
		}
	}))
	defer server.Close()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("invented-key", st, time.Now)
	if _, err := c.Start(ctx, contract.AccountsConnectRequest{Provider: contract.ProviderAnthropic, Label: "Invented"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: "invented-id", Provider: "claude", Record: []byte("{}")}, true); err != nil {
		t.Fatal(err)
	}
	completion := OAuthCompletion{State: "invented-state", AuthID: "invented-id"}
	commit := func(fn func() error) error { return fn() }
	if err := c.Complete(ctx, completion, commit); err != nil {
		t.Fatal("unpolled completion failed")
	}
	if err := c.Complete(ctx, completion, commit); err != nil {
		t.Fatal("completion was not idempotent")
	}
	if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: "interrupted", Provider: "claude", Record: []byte("{}")}, true); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneUnboundProxyAuths(ctx); err != nil {
		t.Fatal(err)
	}
	auths, err := st.ListProxyAuths(ctx)
	if err != nil || len(auths) != 1 || auths[0].ID != "invented-id" {
		t.Fatal("restart did not retain only the bound credential")
	}
}
