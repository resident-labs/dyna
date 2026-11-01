package engine

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

func TestAdoptionRecovery(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	record := store.ProxyAuth{ID: "invented", Provider: "claude", Record: []byte(`{"metadata":{"email":"invented@example.test"},"grant":"old"}`)}
	if err := st.SaveProxyAuth(ctx, record, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", record.ID, contract.ProviderAnthropic, "label"); err != nil {
		t.Fatal(err)
	}
	proxy, err := model.NewProxy(t.TempDir(), "key")
	if err != nil {
		t.Fatal(err)
	}
	setupTestPlanCatalog(t, proxy)
	calls := 0
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v8/management/oauth/auth-url":
			calls++
			_, _ = fmt.Fprintf(w, `{"url":"https://provider.example/login","state":"state%d"}`, calls)
		case "/v8/management/oauth/status":
			adopted := r.URL.Query().Get("state") == "state2"
			_, _ = fmt.Fprintf(w, `{"status":"ok","auth_id":"invented","saved":true,"adopted":%t}`, adopted)
		case "/dyna/credentials/remove":
			if err := st.DeleteProxyAuth(ctx, record.ID); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte("{}"))
		default:
			t.Errorf("unexpected proxy request %s", r.URL.Path)
		}
	})}
	go func() { _ = server.Serve(proxy.Listener) }()
	defer server.Close()
	core := New(st, nil, nil, time.Now)
	connections := proxy.Connections("key", st, time.Now)
	actions := NewAccountActions(core, connections, proxy.Quotas("key", st, time.Now))
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	reconnect, problem := actions.AccountsReconnect(ctx, owner, contract.AccountsReconnectRequest{AccountID: "account"})
	if problem != nil {
		t.Fatal(problem)
	}
	rpc := model.AuthRPC{Store: st, Connections: connections}
	record.Record = []byte(`{"metadata":{"email":"invented@example.test"},"grant":"new"}`)
	if err := rpc.Save(model.AuthSave{Record: record, Create: true, Attempt: reconnect.Attempt, Identity: model.OAuthIdentity{Email: "invented@example.test"}}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	status, problem := actions.AccountsConnectStatus(ctx, owner, contract.AccountsConnectStatusRequest{Attempt: reconnect.Attempt})
	if problem != nil || status.Status != contract.ConnectFailed || status.Reason != contract.ConnectAdoptionFailed {
		t.Fatal("failed adoption was not reported")
	}
	if _, problem := actions.AccountsRemove(ctx, owner, contract.AccountsRemoveRequest{AccountID: "account"}); problem != nil {
		t.Fatal(problem)
	}
	newAttempt, problem := actions.AccountsConnect(ctx, owner, contract.AccountsConnectRequest{Provider: contract.ProviderAnthropic, Label: "replacement"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := rpc.Save(model.AuthSave{Record: record, Create: true, Attempt: newAttempt.Attempt}, &struct{}{}); err != nil {
		t.Fatal("new grant could not be saved")
	}
	status, problem = actions.AccountsConnectStatus(ctx, owner, contract.AccountsConnectStatusRequest{Attempt: newAttempt.Attempt})
	if problem != nil || status.Status != contract.ConnectConnected || status.AccountID == "" {
		t.Fatal("new account did not connect")
	}
}

func TestRemoveFencesNewLogin(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	record := store.ProxyAuth{ID: "invented", Provider: "claude", Record: []byte("{}")}
	if err := st.SaveProxyAuth(ctx, record, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "old-account", "invented", contract.ProviderAnthropic, "Invented"); err != nil {
		t.Fatal(err)
	}
	proxy, err := model.NewProxy(t.TempDir(), "invented-key")
	if err != nil {
		t.Fatal(err)
	}
	deleted, finish := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := st.DeleteProxyAuth(ctx, "invented"); err != nil {
			t.Error(err)
		}
		close(deleted)
		<-finish
		_, _ = w.Write([]byte("{}"))
	})}
	go func() { _ = server.Serve(proxy.Listener) }()
	defer server.Close()
	core := New(st, nil, nil, time.Now)
	actions := NewAccountActions(core, proxy.Connections("invented-key", st, time.Now), proxy.Quotas("invented-key", st, time.Now))
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	done := make(chan *contract.Error, 1)
	go func() {
		_, problem := actions.AccountsRemove(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.AccountsRemoveRequest{AccountID: "old-account"})
		done <- problem
	}()
	<-deleted
	if err := st.SaveProxyAuth(ctx, record, true); err == nil {
		t.Error("new login succeeded before runtime removal")
	}
	close(finish)
	if problem := <-done; problem != nil {
		t.Fatal(problem)
	}
	if err := st.SaveProxyAuth(ctx, record, true); err != nil {
		t.Fatal("new login failed after removal")
	}
}

func TestAccountRemoveRetry(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: "invented", Provider: "claude", Record: []byte("{}")}, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", "invented", contract.ProviderAnthropic, "Invented"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProxyAuth(ctx, "invented"); err != nil {
		t.Fatal(err)
	} // Simulate a crash before the row was deleted.
	proxy, err := model.NewProxy(t.TempDir(), "invented-key")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{}")) })}
	go func() { _ = server.Serve(proxy.Listener) }()
	defer server.Close()
	core := New(st, nil, nil, time.Now)
	actions := NewAccountActions(core, proxy.Connections("invented-key", st, time.Now), proxy.Quotas("invented-key", st, time.Now))
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, problem := actions.AccountsRemove(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.AccountsRemoveRequest{AccountID: "account"}); problem != nil {
		t.Fatal("retry failed")
	}
	if _, err := st.AccountProxyID(ctx, "account"); err == nil {
		t.Fatal("retry left the account row")
	}
}
