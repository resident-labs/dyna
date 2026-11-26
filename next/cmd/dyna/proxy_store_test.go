package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/rpc"
	"testing"
	"time"

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
	if retries, ok := loaded[0].RequestRetryOverride(); !ok || retries != 3 {
		t.Fatalf("existing z.ai key retry override = %d, %v; want 3, true", retries, ok)
	}
	if _, ok := loaded[0].Metadata[proxyauth.DynaRequestQuotaOnly]; ok {
		t.Fatal("existing z.ai key lost its pacing policy")
	}

	created, err := mgr.Register(proxyauth.WithAuthCreationIntent(ctx), &proxyauth.Auth{
		ID:         "dyna-key-fedcba9876543210fedcba9876543210",
		Provider:   "openai-compatible-zai",
		Attributes: map[string]string{"api_key": "invented-new-key"},
		Metadata:   map[string]any{"type": "openai-compatibility"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if retries, ok := created.RequestRetryOverride(); !ok || retries != 3 {
		t.Fatalf("new z.ai key retry override = %d, %v; want 3, true", retries, ok)
	}
}

func TestProxyStoreDoesNotRetainRuntimeAvailability(t *testing.T) {
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

	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(5 * time.Hour)
	quota := proxyauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: reset, BackoffLevel: 3, ObservedAt: now, Signals: map[string]string{"X-Meta-Test": "42"}}
	denial := &proxyauth.Error{HTTPStatus: 429, Message: "quota exhausted"}
	old := &proxyauth.Auth{
		ID: "legacy-credential", Provider: "meta", Metadata: map[string]any{"access_token": "invented-token"},
		Status: proxyauth.StatusError, StatusMessage: denial.Message, LastError: denial, Unavailable: true, NextRetryAfter: reset, Quota: quota,
		ModelStates: map[string]*proxyauth.ModelState{
			"muse":      {Status: proxyauth.StatusError, StatusMessage: denial.Message, LastError: denial, Unavailable: true, NextRetryAfter: reset, Quota: quota},
			"old-model": {Quota: proxyauth.QuotaState{Exceeded: true, NextRecoverAt: reset}},
		},
	}
	blob, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProxyAuth(ctx, store.ProxyAuth{ID: old.ID, Provider: old.Provider, Record: blob}, true); err != nil {
		t.Fatal(err)
	}
	if err := db.BindAccount(ctx, "account", old.ID, contract.ProviderMeta, "Invented"); err != nil {
		t.Fatal(err)
	}
	check := func(auth *proxyauth.Auth) {
		t.Helper()
		checkQuota := func(got proxyauth.QuotaState) {
			t.Helper()
			if got.Exceeded || got.Reason != "" || !got.NextRecoverAt.IsZero() || got.BackoffLevel != 0 || !got.ObservedAt.Equal(now) || got.Signals["X-Meta-Test"] != "42" {
				t.Fatalf("unexpected quota state: %+v", got)
			}
		}
		if auth.Unavailable || auth.LastError != nil || auth.StatusMessage != "" || auth.Status != proxyauth.StatusActive || !auth.NextRetryAfter.IsZero() {
			t.Fatal("credential retains runtime availability state")
		}
		checkQuota(auth.Quota)
		state := auth.ModelStates["muse"]
		if len(auth.ModelStates) != 1 || state == nil || state.Unavailable || state.LastError != nil || state.StatusMessage != "" || state.Status != proxyauth.StatusActive || !state.NextRetryAfter.IsZero() {
			t.Fatal("model retains runtime availability state or lost its observation")
		}
		checkQuota(state.Quota)
	}
	loaded, err := p.List(ctx)
	if err != nil || len(loaded) != 1 || loaded[0].Disabled {
		t.Fatalf("could not load bound legacy credential: %d, %v", len(loaded), err)
	}
	check(loaded[0])
	if _, err := p.Save(ctx, old); err != nil {
		t.Fatal(err)
	}
	records, err := db.ListProxyAuths(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("could not read saved credential: %d, %v", len(records), err)
	}
	var saved proxyauth.Auth
	if err := json.Unmarshal(records[0].Record, &saved); err != nil {
		t.Fatal(err)
	}
	check(&saved)
	if old.LastError != denial || !old.Unavailable || !old.ModelStates["muse"].Unavailable {
		t.Fatal("saving changed the live credential's runtime protections")
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
	if auth.Metadata["access_token"] != "invented-token" || auth.Metadata[proxyauth.DynaRequestQuotaOnly] != true {
		t.Fatal("runtime adoption lost Claude's token or quota policy")
	}
	if _, ok := auth.DisableCoolingOverride(); ok {
		t.Fatal("runtime adoption overrode the upstream non-quota cooling policy")
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
	if loaded = mgr.List(); len(loaded) != 1 || loaded[0].Disabled || loaded[0].Metadata[proxyauth.DynaRequestQuotaOnly] != true {
		t.Fatal("bound credential was not enabled with the right quota policy after restart")
	}
	if err := p.Delete(context.Background(), auth.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Save(context.Background(), auth); err == nil {
		t.Fatal("refresh recreated a deleted credential")
	}
}
