package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/private"
	"github.com/resident-labs/dyna/engine/store"
)

func TestStatusDoesNotBlockReconnectSave(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := private.ProxyAuth{ID: "bound", Provider: "claude", Record: []byte("old")}
	if err := st.SaveProxyAuth(ctx, old, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", old.ID, contract.ProviderAnthropic, "label"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v8/management/oauth/auth-url" {
			_, _ = w.Write([]byte(`{"url":"https://provider.example/login","state":"state"}`))
			return
		}
		close(entered)
		<-release
		_, _ = w.Write([]byte(`{"status":"wait"}`))
	}))
	defer server.Close()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("key", st, time.Now)
	a, err := c.Reconnect(ctx, "account", old.ID, contract.ProviderAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = c.Status(ctx, contract.AccountsConnectStatusRequest{Attempt: a.Attempt}, func(fn func() error) error { return fn() })
		close(done)
	}()
	<-entered
	rpc := AuthRPC{Store: st, Connections: c}
	old.Record = []byte("new")
	if err := rpc.Save(AuthSave{Record: old, Create: true, Attempt: a.Attempt}, &struct{}{}); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	<-done
}

func TestRemoveCancelsUnsavedReconnect(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := private.ProxyAuth{ID: "bound", Provider: "claude", Record: []byte("old")}
	if err := st.SaveProxyAuth(ctx, old, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", old.ID, contract.ProviderAnthropic, "label"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url":"https://provider.example/login","state":"state"}`))
	}))
	defer server.Close()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("key", st, time.Now)
	a, err := c.Reconnect(ctx, "account", old.ID, contract.ProviderAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	c.CancelReconnect("account")
	rpc := AuthRPC{Store: st, Connections: c}
	if err := rpc.Save(AuthSave{Record: old, Create: true, Attempt: a.Attempt}, &struct{}{}); err == nil {
		t.Fatal("cancelled attempt overwrote bound credential")
	}
	if err := st.DeleteProxyAuth(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveAccount(ctx, "account"); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListProxyAuths(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatal("credential remains")
	}
	accounts, err := st.Accounts(ctx)
	if err != nil || len(accounts) != 0 {
		t.Fatal("account remains")
	}
	c.mu.Lock()
	status := c.attempts[a.Attempt].status
	c.mu.Unlock()
	if status != contract.ConnectFailed {
		t.Fatal("attempt remains open")
	}
}

func TestReconnectSaveAttributionAndExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := private.ProxyAuth{ID: "bound", Provider: "claude", Record: []byte("old")}
	if err := st.SaveProxyAuth(ctx, old, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", "bound", contract.ProviderAnthropic, "label"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v8/management/oauth/auth-url":
			_, _ = w.Write([]byte(`{"url":"https://provider.example/login","state":"state"}`))
		case "/v8/management/oauth/status":
			_, _ = w.Write([]byte(`{"status":"ok","auth_id":"bound","saved":true,"adopted":true}`))
		}
	}))
	defer server.Close()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("key", st, func() time.Time { return now })
	a, err := c.Reconnect(ctx, "account", "bound", contract.ProviderAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	rpc := AuthRPC{Store: st, Connections: c}
	next := old
	next.Record = []byte("new")
	if err := rpc.Save(AuthSave{Record: next, Create: true, Attempt: "other"}, &struct{}{}); err == nil {
		t.Fatal("unrelated attempt replaced bound record")
	}
	records, err := st.ListProxyAuths(ctx)
	if err != nil || string(records[0].Record) != "old" {
		t.Fatal("bound record changed")
	}
	now = a.ExpiresAt
	if err := rpc.Save(AuthSave{Record: next, Create: true, Attempt: a.Attempt}, &struct{}{}); err == nil {
		t.Fatal("expired attempt saved")
	}
	now = now.Add(-time.Second)
	if err := rpc.Save(AuthSave{Record: next, Create: true, Attempt: a.Attempt}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	now = a.ExpiresAt
	if err := c.Complete(ctx, OAuthCompletion{State: "state", AuthID: "bound"}, func(fn func() error) error { return fn() }); err != nil {
		t.Fatal(err)
	}
	records, err = st.ListProxyAuths(ctx)
	if err != nil || string(records[0].Record) != "new" || !records[0].Bound {
		t.Fatal("committed grant lost")
	}
}
