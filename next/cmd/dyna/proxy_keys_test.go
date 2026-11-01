package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/rpc"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const keyTestID = "dyna-key-0123456789abcdef0123456789abcdef"

type discoveryFunc func(*http.Request) (*http.Response, error)

func (f discoveryFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeDiscovery answers every model list in a test, so none reaches a provider.
func fakeDiscovery(t *testing.T, respond discoveryFunc) {
	t.Helper()
	previous := modelDiscovery
	modelDiscovery = &http.Client{Transport: respond}
	t.Cleanup(func() { modelDiscovery = previous })
}

func listing(body string) discoveryFunc {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

func keyTestPost(t *testing.T, router *gin.Engine, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body["id"] = keyTestID
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dyna/keys", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer invented")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func customKey() map[string]any {
	return map[string]any{"provider": "custom-acme", "key": "invented-secret-key", "displayName": "Acme", "baseUrl": "http://models.local:8000/v1", "discover": true}
}

func savedModel(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var saved struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	return saved.Model
}

func TestCustomProviderDiscoversItsModelsAndRoutes(t *testing.T) {
	ctx := context.Background()
	manager, service, router := importTestRoutes(t)
	var listed *http.Request
	fakeDiscovery(t, func(r *http.Request) (*http.Response, error) {
		listed = r
		return listing(`{"object":"list","data":[{"id":"local-one"},{"id":"local-two"},{"id":"bad,id"}]}`)(r)
	})
	first := savedModel(t, keyTestPost(t, router, customKey()))
	if first != "custom-acme/local-one" {
		t.Fatalf("first model = %q", first)
	}
	if listed.URL.String() != "http://models.local:8000/v1/models" || listed.Header.Get("Authorization") != "Bearer invented-secret-key" {
		t.Fatalf("listing request = %s", listed.URL)
	}
	if err := service.ActivateDynaCredential(ctx, keyTestID, first); err != nil {
		t.Fatal("the discovered model was not registered")
	}
	defer cliproxy.GlobalModelRegistry().UnregisterClient(keyTestID)
	var seen *http.Request
	manager.SetRoundTripperProvider(importTestTransportProvider{&seen})
	_, _ = manager.Execute(ctx, []string{"openai-compatible-custom-acme"}, executor.Request{Model: first, Payload: []byte(`{"model":"` + first + `","input":"hello"}`)}, executor.Options{SourceFormat: translator.FormatOpenAIResponse})
	if seen == nil || seen.URL.Host != "models.local:8000" || seen.Header.Get("Authorization") != "Bearer invented-secret-key" {
		t.Fatal("request did not reach the owner's address")
	}
	body, err := io.ReadAll(seen.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"model":"local-one"`) {
		t.Fatalf("upstream model = %s", body)
	}
}

// A provider that lists a built-in's model must not take that model's traffic.
func TestCustomProviderModelsCannotShadowBuiltIns(t *testing.T) {
	_, service, router := importTestRoutes(t)
	fakeDiscovery(t, listing(`{"data":[{"id":"claude-sonnet-5"}]}`))
	first := savedModel(t, keyTestPost(t, router, customKey()))
	if err := service.ActivateDynaCredential(context.Background(), keyTestID, first); err != nil {
		t.Fatal("activation failed")
	}
	defer cliproxy.GlobalModelRegistry().UnregisterClient(keyTestID)
	registry := cliproxy.GlobalModelRegistry()
	if !registry.ClientSupportsModel(keyTestID, "custom-acme/claude-sonnet-5") || registry.ClientSupportsModel(keyTestID, "claude-sonnet-5") {
		t.Fatal("a custom provider registered a bare model")
	}
}

func TestProviderWithoutAModelListTakesHandEnteredModels(t *testing.T) {
	_, _, router := importTestRoutes(t)
	fakeDiscovery(t, func(*http.Request) (*http.Response, error) { return nil, errors.New("no route") })
	if response := keyTestPost(t, router, customKey()); response.Code != http.StatusUnprocessableEntity || response.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %d bytes", response.Code, response.Body.Len())
	}
	hand := customKey()
	hand["discover"], hand["models"] = false, []string{"typed-model"}
	if first := savedModel(t, keyTestPost(t, router, hand)); first != "custom-acme/typed-model" {
		t.Fatalf("first model = %q", first)
	}
}

func TestPresetKeysKeepSeedsWhenDiscoveryFails(t *testing.T) {
	_, _, router := importTestRoutes(t)
	fakeDiscovery(t, listing(`not json`))
	body := map[string]any{"provider": "deepseek", "key": "invented-key", "displayName": "DeepSeek", "baseUrl": "https://api.deepseek.com/v1", "models": []string{"deepseek-chat"}, "discover": true}
	if first := savedModel(t, keyTestPost(t, router, body)); first != "deepseek-chat" {
		t.Fatalf("first model = %q", first)
	}
}

func TestProviderKeyStaysOutOfResponsesAndLogs(t *testing.T) {
	var logged bytes.Buffer
	previous := log.StandardLogger().Out
	log.SetOutput(&logged)
	stdlog.SetOutput(&logged)
	defer func() {
		log.SetOutput(previous)
		stdlog.SetOutput(previous)
	}()
	manager, service, router := importTestRoutes(t)
	fakeDiscovery(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connect refused with invented-secret-key")
	})
	refused := keyTestPost(t, router, customKey())
	hand := customKey()
	hand["discover"], hand["models"] = false, []string{"typed-model"}
	saved := keyTestPost(t, router, hand)
	if err := service.ActivateDynaCredential(context.Background(), keyTestID, "custom-acme/typed-model"); err != nil {
		t.Fatal("activation failed")
	}
	defer cliproxy.GlobalModelRegistry().UnregisterClient(keyTestID)
	listed, err := json.Marshal(service.DynaCredentials())
	if err != nil {
		t.Fatal(err)
	}
	var seen *http.Request
	manager.SetRoundTripperProvider(importTestTransportProvider{&seen})
	_, _ = manager.Execute(context.Background(), []string{"openai-compatible-custom-acme"}, executor.Request{Model: "custom-acme/typed-model", Payload: []byte(`{"model":"custom-acme/typed-model","input":"hello"}`)}, executor.Options{SourceFormat: translator.FormatOpenAIResponse})
	for name, text := range map[string]string{"refusal": refused.Body.String(), "response": saved.Body.String(), "credential list": string(listed), "logs": logged.String()} {
		if strings.Contains(text, "invented-secret-key") {
			t.Fatalf("the key reached the %s", name)
		}
	}
}

// Accounts created before providers held their models must keep their URL and models.
func TestExistingKeyAccountsSurviveTheMoveToPresets(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	existing := []struct {
		id, proxy, name string
		provider        contract.AccountProvider
		host, model     string
	}{
		{"dyna-key-00000000000000000000000000000001", "openai-compatible-zai", "zai", contract.ProviderZAI, "api.z.ai", "glm-5.3"},
		{"dyna-key-00000000000000000000000000000002", "openai-compatible-opencode-go", "opencode-go", contract.ProviderOpencodeGo, "opencode.ai", "kimi-k3"},
		{"dyna-key-00000000000000000000000000000003", "openai-compatible-deepseek", "deepseek", contract.ProviderDeepSeek, "api.deepseek.com", "deepseek-chat"},
	}
	for _, account := range existing {
		preset, ok := model.PresetForProxy(account.proxy)
		if !ok {
			t.Fatalf("no preset for %s", account.proxy)
		}
		// The record as older code saved it: no models and no display name.
		record, err := json.Marshal(coreauth.Auth{ID: account.id, Provider: account.proxy, Label: account.name, Status: coreauth.StatusActive, Attributes: map[string]string{
			"api_key": "invented-" + account.name, "auth_kind": "apikey", "base_url": preset.URL, "compat_name": account.name, "provider_key": account.proxy,
		}, Metadata: map[string]any{"type": "openai-compatibility"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveProxyAuth(ctx, store.ProxyAuth{ID: account.id, Provider: account.proxy, Record: record}, true); err != nil {
			t.Fatal(err)
		}
		if err := db.BindAccount(ctx, contract.AccountID(account.name), account.id, account.provider, account.name); err != nil {
			t.Fatal(err)
		}
	}
	server, client := net.Pipe()
	r := rpc.NewServer()
	if err := r.RegisterName("Auth", &model.AuthRPC{Store: db}); err != nil {
		t.Fatal(err)
	}
	go r.ServeConn(server)
	p := &proxyStore{client: rpc.NewClient(client)}
	defer p.client.Close()
	// A custom provider must come back too, with its prefix and own models.
	const customID = "dyna-key-00000000000000000000000000000004"
	custom, err := json.Marshal(coreauth.Auth{ID: customID, Provider: "openai-compatible-custom-acme", Prefix: "custom-acme", Status: coreauth.StatusActive, Attributes: map[string]string{
		"api_key": "invented-custom", "auth_kind": "apikey", "base_url": "http://models.local/v1", "compat_name": "custom-acme", "provider_key": "openai-compatible-custom-acme",
		cliproxy.DynaModelsAttribute: "local-one", cliproxy.DynaDisplayNameAttribute: "Acme",
	}, Metadata: map[string]any{"type": "openai-compatibility"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProxyAuth(ctx, store.ProxyAuth{ID: customID, Provider: "openai-compatible-custom-acme", Record: custom}, true); err != nil {
		t.Fatal(err)
	}
	if err := db.BindAccount(ctx, "custom", customID, "custom-acme", "custom"); err != nil {
		t.Fatal(err)
	}
	loaded, err := p.List(ctx)
	if err != nil || len(loaded) != len(existing)+1 {
		t.Fatalf("loaded %d credentials: %v", len(loaded), err)
	}
	manager, service, _ := importTestRoutes(t)
	var seen *http.Request
	manager.SetRoundTripperProvider(importTestTransportProvider{&seen})
	for _, auth := range loaded {
		if _, err := manager.Register(coreauth.WithSkipPersist(ctx), auth); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.ActivateDynaCredential(ctx, customID, "custom-acme/local-one"); err != nil {
		t.Fatal("the custom provider lost its model")
	}
	defer cliproxy.GlobalModelRegistry().UnregisterClient(customID)
	for _, account := range existing {
		if err := service.ActivateDynaCredential(ctx, account.id, account.model); err != nil {
			t.Fatalf("%s lost its model %s", account.name, account.model)
		}
		defer cliproxy.GlobalModelRegistry().UnregisterClient(account.id)
		seen = nil
		_, _ = manager.Execute(ctx, []string{account.proxy}, executor.Request{Model: account.model, Payload: []byte(`{"model":"` + account.model + `","input":"hello"}`)}, executor.Options{SourceFormat: translator.FormatOpenAIResponse})
		if seen == nil || seen.URL.Host != account.host || seen.Header.Get("Authorization") != "Bearer invented-"+account.name {
			t.Fatalf("%s did not reach its provider", account.name)
		}
	}
}
