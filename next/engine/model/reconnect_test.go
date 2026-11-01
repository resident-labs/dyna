package model

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/private"
	"github.com/resident-labs/dyna/engine/store"
)

func TestReconnectRequiresRawMatchingIdentity(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := private.ProxyAuth{ID: "xai-collision.json", Provider: "xai", Record: []byte(`{"metadata":{"email":"a+b@example.test","sub":"one"}}`)}
	if err := st.SaveProxyAuth(ctx, old, true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"url":"https://provider.example/login","state":"state"}`))
	}))
	defer server.Close()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	c := p.Connections("key", st, time.Now)
	a, err := c.Reconnect(ctx, "account", old.ID, contract.ProviderXAI)
	if err != nil {
		t.Fatal(err)
	}
	next := old
	next.Record = []byte(`{"metadata":{"email":"a-b@example.test","sub":"two"}}`)
	rpc := AuthRPC{Store: st, Connections: c}
	if err := rpc.Save(AuthSave{Record: next, Create: true, Attempt: a.Attempt, Identity: OAuthIdentity{Subject: "two"}}, &struct{}{}); err == nil {
		t.Fatal("different identity replaced the saved credential")
	}
	rows, err := st.ListProxyAuths(ctx)
	if err != nil || string(rows[0].Record) != string(old.Record) {
		t.Fatal("saved credential changed")
	}
	status, err := c.Status(ctx, contract.AccountsConnectStatusRequest{Attempt: a.Attempt}, nil)
	if err != nil || status.Status != contract.ConnectFailed || status.Reason != contract.ConnectWrongAccount {
		t.Fatal("wrong account did not produce a typed failure")
	}
	a, err = c.Reconnect(ctx, "account", old.ID, contract.ProviderXAI)
	if err != nil {
		t.Fatal(err)
	}
	next.Record = []byte(`{"metadata":{}}`)
	if err := rpc.Save(AuthSave{Record: next, Create: true, Attempt: a.Attempt}, &struct{}{}); err == nil {
		t.Fatal("new grant without identity replaced the saved credential")
	}
	missing := private.ProxyAuth{ID: "no-identity", Provider: "kimi", Record: []byte(`{"metadata":{"type":"kimi"}}`)}
	if err := st.SaveProxyAuth(ctx, missing, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reconnect(ctx, "other", missing.ID, contract.ProviderXAI); err == nil {
		t.Fatal("started reconnect without saved identity")
	}
	if calls != 2 {
		t.Fatal("started provider login without saved identity")
	}
}

type pausedReconnectStore struct {
	*store.Store
	entered, release chan struct{}
}

func (s pausedReconnectStore) SaveProxyAuth(ctx context.Context, record private.ProxyAuth, create bool) error {
	if !create {
		close(s.entered)
		<-s.release
	}
	return s.Store.SaveProxyAuth(ctx, record, create)
}

func TestReconnectPreventsConcurrentReplacement(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := private.ProxyAuth{ID: "bound", Provider: "claude", Record: []byte(`{"metadata":{"email":"invented@example.test"},"grant":"old"}`)}
	if err := st.SaveProxyAuth(ctx, old, true); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url":"https://provider.example/login","state":"state"}`))
	}))
	defer server.Close()
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	paused := pausedReconnectStore{Store: st, entered: make(chan struct{}), release: make(chan struct{})}
	c := p.Connections("key", paused, time.Now)
	a, err := c.Reconnect(ctx, "account", old.ID, contract.ProviderAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reconnect(ctx, "account", old.ID, contract.ProviderAnthropic); err == nil {
		t.Fatal("concurrent reconnect started")
	}
	rpc := AuthRPC{Store: paused, Connections: c}
	old.Record = []byte(`{"metadata":{"email":"invented@example.test"},"grant":"new"}`)
	saved := make(chan error, 1)
	go func() {
		saved <- rpc.Save(AuthSave{Record: old, Create: true, Attempt: a.Attempt, Identity: OAuthIdentity{Email: "invented@example.test"}}, &struct{}{})
	}()
	<-paused.entered
	started := make(chan error, 1)
	go func() { _, err := c.Reconnect(ctx, "account", old.ID, contract.ProviderAnthropic); started <- err }()
	close(paused.release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if err := <-started; err == nil {
		t.Fatal("reconnect started before saved grant adoption")
	}
}

func TestStatusDoesNotBlockReconnectSave(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := private.ProxyAuth{ID: "bound", Provider: "claude", Record: []byte(`{"metadata":{"email":"invented@example.test"},"grant":"old"}`)}
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
	old.Record = []byte(`{"metadata":{"email":"invented@example.test"},"grant":"new"}`)
	if err := rpc.Save(AuthSave{Record: old, Create: true, Attempt: a.Attempt, Identity: OAuthIdentity{Email: "invented@example.test"}}, &struct{}{}); err != nil {
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
	old := private.ProxyAuth{ID: "bound", Provider: "claude", Record: []byte(`{"metadata":{"email":"invented@example.test"},"grant":"old"}`)}
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
	if err := rpc.Save(AuthSave{Record: old, Create: true, Attempt: a.Attempt, Identity: OAuthIdentity{Email: "invented@example.test"}}, &struct{}{}); err == nil {
		t.Fatal("cancelled attempt overwrote bound credential")
	}
	if err := st.DeleteProxyAuth(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveAccount(ctx, "account"); err != nil {
		t.Fatal(err)
	}
	if err := rpc.Save(AuthSave{Record: old, Create: true, Attempt: a.Attempt, Identity: OAuthIdentity{Email: "invented@example.test"}}, &struct{}{}); err == nil {
		t.Fatal("late reconnect recreated a removed credential")
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
	old := private.ProxyAuth{ID: "bound", Provider: "claude", Record: []byte(`{"metadata":{"email":"invented@example.test"},"grant":"old"}`)}
	if err := st.SaveProxyAuth(ctx, old, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", "bound", contract.ProviderAnthropic, "label"); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('room','room',0,1,1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountRooms(ctx, "account", []contract.RoomID{"room"}); err != nil {
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
	next.Record = []byte(`{"metadata":{"email":"invented@example.test"},"grant":"new"}`)
	if err := rpc.Save(AuthSave{Record: next, Create: true, Attempt: "other"}, &struct{}{}); err == nil {
		t.Fatal("unrelated attempt replaced bound record")
	}
	records, err := st.ListProxyAuths(ctx)
	if err != nil || string(records[0].Record) != string(old.Record) {
		t.Fatal("bound record changed")
	}
	now = a.ExpiresAt
	if err := rpc.Save(AuthSave{Record: next, Create: true, Attempt: a.Attempt, Identity: OAuthIdentity{Email: "invented@example.test"}}, &struct{}{}); err == nil {
		t.Fatal("expired attempt saved")
	}
	now = now.Add(-time.Second)
	if err := rpc.Save(AuthSave{Record: next, Create: true, Attempt: a.Attempt, Identity: OAuthIdentity{Email: "invented@example.test"}}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	now = a.ExpiresAt
	if err := c.Complete(ctx, OAuthCompletion{State: "state", AuthID: "bound"}, func(fn func() error) error { return fn() }); err != nil {
		t.Fatal(err)
	}
	records, err = st.ListProxyAuths(ctx)
	if err != nil || string(records[0].Record) != string(next.Record) || !records[0].Bound {
		t.Fatal("committed grant lost")
	}
	accounts, err := st.Accounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].Account.Label != "label" || len(accounts[0].Account.Rooms) != 1 || accounts[0].Account.Rooms[0] != "room" {
		t.Fatal("reconnect changed account label or rooms")
	}
}

func TestSameAccountComparesStrongestSharedField(t *testing.T) {
	imported := OAuthIdentity{Email: "Owner@example.test"}
	for _, test := range []struct {
		name         string
		saved, fresh OAuthIdentity
		want         bool
	}{
		{"imported email matches a fresh sign-in", imported, OAuthIdentity{AccountUUID: "new", Email: "owner@example.test"}, true},
		{"imported email rejects another person", imported, OAuthIdentity{AccountUUID: "new", Email: "other@example.test"}, false},
		{"a shared ID outranks a matching email", OAuthIdentity{AccountUUID: "one", Email: "a@example.test"}, OAuthIdentity{AccountUUID: "two", Email: "a@example.test"}, false},
		{"nothing shared is no match", OAuthIdentity{AccountUUID: "one"}, OAuthIdentity{Subject: "two"}, false},
		{"nothing saved is no match", OAuthIdentity{}, OAuthIdentity{Email: "a@example.test"}, false},
	} {
		if got := sameAccount(test.saved, test.fresh); got != test.want {
			t.Errorf("%s: got %v", test.name, got)
		}
	}
}
