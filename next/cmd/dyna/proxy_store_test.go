package main

import (
	"context"
	"net"
	"net/rpc"
	"testing"

	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
	proxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type inventedToken struct {
	AccessToken string `json:"access_token"`
}

func (*inventedToken) SaveTokenToFile(string) error { panic("must not write a file") }

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
	if len(loaded) != 1 || loaded[0].Metadata["access_token"] != "invented-token" {
		t.Fatal("restart lost credential")
	}
	if err := p.Delete(context.Background(), auth.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Save(context.Background(), auth); err == nil {
		t.Fatal("refresh recreated a deleted credential")
	}
}
