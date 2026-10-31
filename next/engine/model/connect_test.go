package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestProviderFlowsMatchOAuthDispatch(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.URL.Query().Get("provider")
		_, _ = w.Write([]byte(`{"url":"https://provider.example/login","state":"invented"}`))
	}))
	defer server.Close()
	proxy := &Proxy{client: &http.Client{Transport: quotaTestTransport{url: server.URL, base: http.DefaultTransport}}}
	connections := proxy.Connections("invented", nil, time.Now)
	for _, option := range oauthProviders {
		result, err := connections.Start(context.Background(), contract.AccountsConnectRequest{Provider: option.info.ID})
		if err != nil || result.Flow != option.info.Flows[0] || <-received != option.proxy {
			t.Fatalf("flow dispatch disagreed with catalog for %s", option.info.ID)
		}
	}
}

func TestConnectBindsAttemptsAndNeverExposesProxyID(t *testing.T) {
	starts, callbacks := 0, 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer invented-management-key" {
			t.Error("proxy was not authenticated")
		}
		switch r.URL.Path {
		case "/v8/management/oauth/auth-url":
			starts++
			fmt.Fprintf(w, `{"url":"https://provider.example/login","state":"state-%d"}`, starts)
		case "/v8/management/oauth/callback":
			callbacks++
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/oauth/status":
			_ = json.NewEncoder(w).Encode(struct {
				Status  string `json:"status"`
				AuthID  string `json:"auth_id"`
				Saved   bool   `json:"saved"`
				Adopted bool   `json:"adopted"`
			}{"ok", "private-proxy-id", true, true})
		default:
			t.Error("unexpected proxy call")
		}
	}))
	defer proxy.Close()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Unix(1000, 0)
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{url: proxy.URL, base: http.DefaultTransport}}}
	c := p.Connections("invented-management-key", st, func() time.Time { return now })
	ctx := context.Background()
	if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: "private-proxy-id", Provider: "claude", Record: []byte("{}")}, true); err != nil {
		t.Fatal(err)
	}
	a, err := c.Start(ctx, contract.AccountsConnectRequest{Provider: contract.ProviderAnthropic, Label: "Invented"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Start(ctx, contract.AccountsConnectRequest{Provider: contract.ProviderOpenAI, Label: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Callback(ctx, contract.AccountsConnectCallbackRequest{Attempt: a.Attempt, RedirectURL: "http://localhost/callback?state=state-2&code=invented"}); err == nil {
		t.Fatal("accepted another attempt's state")
	}
	if err := c.Callback(ctx, contract.AccountsConnectCallbackRequest{Attempt: a.Attempt, RedirectURL: "http://localhost/callback?state=state-1&code=invented"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Callback(ctx, contract.AccountsConnectCallbackRequest{Attempt: a.Attempt, RedirectURL: "http://localhost/callback?state=state-1&code=invented"}); err == nil || callbacks != 1 {
		t.Fatal("callback was forwarded twice")
	}
	commit := func(fn func() error) error { return fn() }
	status, err := c.Status(ctx, contract.AccountsConnectStatusRequest{Attempt: a.Attempt}, commit)
	if err != nil || status.Status != contract.ConnectConnected || status.AccountID == "" || string(status.AccountID) == "private-proxy-id" {
		t.Fatal("connection was not bound privately")
	}
	again, err := c.Status(ctx, contract.AccountsConnectStatusRequest{Attempt: a.Attempt}, commit)
	if err != nil || again.AccountID != status.AccountID {
		t.Fatal("completion was not idempotent")
	}
	p.generation.Add(1)
	status, err = c.Status(ctx, contract.AccountsConnectStatusRequest{Attempt: b.Attempt}, commit)
	if err != nil || status.Status != contract.ConnectFailed {
		t.Fatal("restart did not fail pending attempt")
	}
}
