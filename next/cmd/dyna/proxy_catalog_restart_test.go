package main

import (
	"context"
	"encoding/json"
	"net"
	"net/rpc"
	"testing"

	"github.com/resident-labs/dyna/engine/store"
	proxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type catalogAuthRPC struct{ records []store.ProxyAuth }

func (r *catalogAuthRPC) List(_ struct{}, out *[]store.ProxyAuth) error { *out = r.records; return nil }

func TestProxyRestartRebuildsCatalogAndPlanNamespaces(t *testing.T) {
	record := func(id, provider string, attrs map[string]string) store.ProxyAuth {
		raw, err := json.Marshal(&proxyauth.Auth{ID: id, Provider: provider, Attributes: attrs, Status: proxyauth.StatusActive})
		if err != nil {
			t.Fatal(err)
		}
		return store.ProxyAuth{ID: id, Provider: provider, Record: raw, Bound: true}
	}
	rpcServer := rpc.NewServer()
	records := []store.ProxyAuth{
		record("dyna-key-legacy", "openai-compatible-deepseek", map[string]string{"compat_name": "deepseek", "base_url": "https://old.invalid", "api_key": "invented-key"}),
		record("dyna-key-removed", "openai-compatible-removed", map[string]string{"dyna_provider": "removed", "api_key": "invented-key"}),
		record("plan", "claude", nil),
	}
	if err := rpcServer.RegisterName("Auth", &catalogAuthRPC{records}); err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	go rpcServer.ServeConn(server)
	p := &proxyStore{client: rpc.NewClient(client), catalog: []config.OpenAICompatibility{{Name: "deepseek", BaseURL: "https://new.example/v1"}}}
	defer p.client.Close()
	auths, err := p.List(context.Background())
	if err != nil || len(auths) != 3 {
		t.Fatalf("auths=%v err=%v", auths, err)
	}
	if auths[0].Prefix != "deepseek" || auths[0].Attributes["base_url"] != "https://new.example/v1" || auths[0].Attributes["api_key"] != "invented-key" || auths[0].Disabled {
		t.Fatal("legacy key did not rebuild from the current catalog")
	}
	if !auths[1].Disabled {
		t.Fatal("removed provider remained routable")
	}
	if auths[2].Prefix != "plan_anthropic" || auths[2].Disabled {
		t.Fatal("plan namespace was not restored")
	}
}
