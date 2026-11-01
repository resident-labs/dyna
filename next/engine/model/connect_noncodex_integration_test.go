//go:build dyna_integration

package model

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/rpc"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type integrationCredentialStore struct{ client *rpc.Client }

func (s integrationCredentialStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (s integrationCredentialStore) Delete(ctx context.Context, id string) error {
	return s.client.Call("Auth.Delete", AuthDelete{ID: id}, &struct{}{})
}
func (s integrationCredentialStore) Save(ctx context.Context, a *coreauth.Auth) (string, error) {
	raw, err := json.Marshal(a.Storage)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, &a.Metadata); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	err = s.client.Call("Auth.Save", AuthSave{Record: store.ProxyAuth{ID: a.ID, Provider: a.Provider, Record: encoded}, Create: true}, &struct{}{})
	return a.ID, err
}

func TestKimiCompletionBindsAcrossProxyNotification(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var connections *Connections
	serverConn, clientConn := net.Pipe()
	rpcServer := rpc.NewServer()
	if err := rpcServer.RegisterName("Auth", &AuthRPC{Store: st, Complete: func(ctx context.Context, done OAuthCompletion) error {
		return connections.Complete(ctx, done, func(fn func() error) error { return fn() })
	}}); err != nil {
		t.Fatal(err)
	}
	go rpcServer.ServeConn(serverConn)
	client := rpc.NewClient(clientConn)
	defer client.Close()
	var completionErr error
	harness := cliproxy.NewDynaIntegrationHarness(integrationCredentialStore{client}, func(state, id string) {
		var result OAuthCompletionResult
		completionErr = client.Call("Auth.OAuthCompleted", OAuthCompletion{State: state, AuthID: id}, &result)
		if completionErr == nil && !result.Accepted {
			completionErr = ErrCompletionRejected
		}
	})
	defer harness.Close()
	server := httptest.NewServer(harness)
	defer server.Close()
	p := &Proxy{pools: testPools(), client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}
	connections = p.Connections("invented-key", st, time.Now)
	if _, err := connections.Start(ctx, contract.AccountsConnectRequest{Provider: contract.ProviderKimiCoding, Label: "Invented"}); err != nil {
		t.Fatal(err)
	}
	if err := harness.Complete(); err != nil {
		t.Fatal(err)
	}
	if completionErr != nil {
		t.Fatal("proxy completion did not bind the account")
	}
	records, err := st.ListProxyAuths(ctx)
	if err != nil || len(records) != 1 || !records[0].Bound {
		t.Fatal("completed Kimi credential is not bound")
	}
}
