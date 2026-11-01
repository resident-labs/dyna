package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/rpc"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
	proxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type inventedToken struct {
	AccessToken string `json:"access_token"`
}

func (*inventedToken) SaveTokenToFile(string) error { panic("must not write a file") }

type captureIdentityRPC struct{ saved model.AuthSave }

func (r *captureIdentityRPC) Save(req model.AuthSave, _ *struct{}) error { r.saved = req; return nil }

func TestProxyStoreForwardsRawIdentity(t *testing.T) {
	server, client := net.Pipe()
	r := rpc.NewServer()
	capture := &captureIdentityRPC{}
	if err := r.RegisterName("Auth", capture); err != nil {
		t.Fatal(err)
	}
	go r.ServeConn(server)
	p := &proxyStore{client: rpc.NewClient(client)}
	defer p.client.Close()
	info := &proxyauth.RequestInfo{Headers: http.Header{"X-Dyna-Attempt": {"attempt"}}, OAuthIdentity: &proxyauth.OAuthIdentity{Email: "new@example.test"}}
	ctx := proxyauth.WithAuthCreationIntent(proxyauth.WithRequestInfo(context.Background(), info))
	auth := &proxyauth.Auth{ID: "invented", Provider: "claude", Metadata: map[string]any{"email": "old@example.test"}}
	if _, err := p.Save(ctx, auth); err != nil {
		t.Fatal(err)
	}
	if capture.saved.Identity.Email != "new@example.test" || capture.saved.Attempt != "attempt" {
		t.Fatal("raw identity was not forwarded")
	}
}

func TestProxyStoreLoadsKeyEndpointFromCatalog(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// An account created before the catalog moved z.ai to its coding endpoint.
	const id = "dyna-key-0123456789abcdef0123456789abcdef"
	record, err := json.Marshal(proxyauth.Auth{ID: id, Provider: "openai-compatible-zai", Attributes: map[string]string{"api_key": "invented-key", "base_url": "https://api.z.ai/api/paas/v4", "compat_name": "zai"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProxyAuth(ctx, store.ProxyAuth{ID: id, Provider: "openai-compatible-zai", Record: record}, true); err != nil {
		t.Fatal(err)
	}
	if err := db.BindAccount(ctx, "account", id, contract.ProviderZAI, "Invented"); err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	r := rpc.NewServer()
	if err := r.RegisterName("Auth", &model.AuthRPC{Store: db}); err != nil {
		t.Fatal(err)
	}
	go r.ServeConn(server)
	p := &proxyStore{client: rpc.NewClient(client)}
	defer p.client.Close()
	mgr := proxyauth.NewManager(p, nil, nil)
	if err := mgr.Load(ctx); err != nil {
		t.Fatal(err)
	}
	loaded := mgr.List()
	if len(loaded) != 1 {
		t.Fatalf("loaded %d credentials", len(loaded))
	}
	if got := loaded[0].Attributes["base_url"]; got != "https://api.z.ai/api/coding/paas/v4" {
		t.Fatalf("stored z.ai account still uses %s", got)
	}
	if loaded[0].Attributes["api_key"] != "invented-key" {
		t.Fatal("loading the endpoint lost the key")
	}
}

func TestProxyStoreReloadsCredentialAndRejectsDuplicate(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, client := net.Pipe()
	r := rpc.NewServer()
	if err := r.RegisterName("Auth", &model.AuthRPC{Store: db}); err != nil {
		t.Fatal(err)
	}
	go r.ServeConn(server)
	p := &proxyStore{client: rpc.NewClient(client)}
	defer p.client.Close()
	ctx := proxyauth.WithAuthCreationIntent(context.Background())
	auth := &proxyauth.Auth{ID: "invented", Provider: "claude", Storage: &inventedToken{AccessToken: "invented-token"}}
	if _, err := p.Save(ctx, auth); err != nil {
		t.Fatal(err)
	}
	if auth.Metadata["access_token"] != "invented-token" {
		t.Fatal("runtime adoption lost token")
	}
	if _, err := p.Save(ctx, auth); err == nil {
		t.Fatal("duplicate sign-in replaced a credential")
	}
	mgr := proxyauth.NewManager(p, nil, nil)
	if err := mgr.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded := mgr.List()
	if len(loaded) != 1 || !loaded[0].Disabled || loaded[0].Metadata["access_token"] != "invented-token" {
		t.Fatal("restart lost credential")
	}
	if err := db.BindAccount(context.Background(), "account", auth.ID, "anthropic", "Invented"); err != nil {
		t.Fatal(err)
	}
	mgr = proxyauth.NewManager(p, nil, nil)
	if err := mgr.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loaded = mgr.List(); len(loaded) != 1 || loaded[0].Disabled {
		t.Fatal("bound credential was not enabled after restart")
	}
	if err := p.Delete(context.Background(), auth.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Save(context.Background(), auth); err == nil {
		t.Fatal("refresh recreated a deleted credential")
	}
}
