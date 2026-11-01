package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestPresetsKeepTheirEndpoints(t *testing.T) {
	want := map[contract.AccountProvider]string{
		contract.ProviderZAI:        "https://api.z.ai/api/coding/paas/v4",
		contract.ProviderOpencodeGo: "https://opencode.ai/inference/go/v1",
		contract.ProviderDeepSeek:   "https://api.deepseek.com/v1",
	}
	for _, preset := range KeyPresets() {
		if preset.URL != want[preset.Provider] || len(preset.Models) == 0 {
			t.Fatalf("preset = %#v", preset)
		}
		if found, ok := PresetForProxy("openai-compatible-" + string(preset.Provider)); !ok || found.URL != preset.URL {
			t.Fatalf("preset %s is not found by its proxy provider", preset.Provider)
		}
		delete(want, preset.Provider)
	}
	if len(want) != 0 {
		t.Fatalf("missing presets: %v", want)
	}
}

func TestCustomProviderNamesCannotShadowBuiltIns(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   contract.AccountProvider
	}{
		{"Anthropic", "custom-anthropic"},
		{"kimi", "custom-kimi"},
		{"  My   Lab!  ", "custom-my-lab"},
		{"openai-compatible-zai", "custom-openai-compatible-zai"},
	} {
		if got := customProviderID(tc.name); got != tc.id {
			t.Errorf("%q = %q, want %q", tc.name, got, tc.id)
		}
	}
	if id := customProviderID("模型"); !strings.HasPrefix(string(id), CustomProviderPrefix) || len(id) <= len(CustomProviderPrefix) {
		t.Fatalf("a name with no ASCII gave %q", id)
	}
	for _, provider := range AccountProviders(nil) {
		for _, name := range []string{provider.DisplayName, string(provider.ID), strings.ToUpper(provider.DisplayName)} {
			if ValidNewProvider(name, "https://models.example/v1") {
				t.Errorf("%q took a built-in's name", name)
			}
		}
	}
	if IsCustomProxyProvider("openai-compatible-zai") || !IsCustomProxyProvider("openai-compatible-custom-zai") {
		t.Fatal("preset and custom proxy providers are confused")
	}
}

// The owner may point a provider at anything, so an address only has to parse.
func TestNewProviderAddressOnlyHasToParse(t *testing.T) {
	for _, address := range []string{"https://models.example/v1", "http://localhost:11434/v1", "http://192.168.1.20:8000/v1", "http://100.64.0.1/v1", "http://[::1]:8080/v1"} {
		if !ValidNewProvider("Local", address) {
			t.Errorf("%q was refused", address)
		}
	}
	for _, address := range []string{"", "models.example", "ftp://models.example", "http://", "://x", "http://bad host/"} {
		if ValidNewProvider("Local", address) {
			t.Errorf("%q was accepted", address)
		}
	}
	for _, name := range []string{"", "   ", "two\nlines", strings.Repeat("x", 65)} {
		if ValidNewProvider(name, "https://models.example/v1") {
			t.Errorf("name %q was accepted", name)
		}
	}
}

func TestCleanModelsKeepsOnlyListableIDs(t *testing.T) {
	got := cleanModels([]string{" one ", "two", "one", "", "a,b", "a b"})
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("models = %v", got)
	}
}

func TestAccountProvidersAddSavedCustomProviders(t *testing.T) {
	providers := AccountProviders([]Credential{
		{Provider: "openai-compatible-custom-zeta", DisplayName: "Zeta"},
		{Provider: "openai-compatible-custom-acme", DisplayName: "Acme"},
		{Provider: "openai-compatible-custom-acme", DisplayName: "Acme"},
		{Provider: "openai-compatible-zai"},
		{Provider: "claude"},
	})
	builtIn := len(AccountProviders(nil))
	if len(providers) != builtIn+2 || providers[builtIn].ID != "custom-acme" || providers[builtIn+1].ID != "custom-zeta" {
		t.Fatalf("providers = %#v", providers[builtIn:])
	}
	if p := providers[builtIn]; p.DisplayName != "Acme" || p.ConnectFlow != contract.ConnectFlowKey || p.ConnectMethod != contract.ConnectKey {
		t.Fatalf("provider = %#v", p)
	}
}

type keyProxy struct {
	store       *store.Store
	credentials string
	status      int
	body        map[string]any
	activated   string
}

func (p *keyProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/dyna/credentials":
		fmt.Fprintf(w, `{"credentials":[%s]}`, p.credentials)
	case "/dyna/keys":
		p.body = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&p.body)
		if p.status != 0 {
			w.WriteHeader(p.status)
			return
		}
		id, _ := p.body["id"].(string)
		if err := p.store.SaveProxyAuth(r.Context(), store.ProxyAuth{ID: id, Provider: "invented", Record: []byte("{}")}, true); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"model":"custom-acme/one"}`)
	case "/dyna/credentials/activate":
		var input struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		p.activated = input.Model
		fmt.Fprint(w, `{}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func keyConnections(t *testing.T, proxy *keyProxy) (*Connections, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	proxy.store = st
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	p := &Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, http.DefaultTransport}}}
	return p.Connections("key", st, time.Now), st
}

func TestSetKeyAddsAProviderAndDiscoversItsModels(t *testing.T) {
	proxy := &keyProxy{}
	c, st := keyConnections(t, proxy)
	ctx := context.Background()
	out, err := c.SetKey(ctx, contract.AccountsSetKeyRequest{DisplayName: " Acme ", BaseURL: "http://localhost:11434/v1", Label: "Local", Key: "invented-key"})
	if err != nil || out.AccountID == "" {
		t.Fatalf("set key: %v", err)
	}
	if proxy.body["provider"] != "custom-acme" || proxy.body["displayName"] != "Acme" || proxy.body["baseUrl"] != "http://localhost:11434/v1" || proxy.body["discover"] != true {
		t.Fatalf("proxy body = %#v", proxy.body)
	}
	if proxy.activated != "custom-acme/one" {
		t.Fatalf("activated %q", proxy.activated)
	}
	accounts, err := st.Accounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].Account.Provider != "custom-acme" {
		t.Fatalf("accounts = %#v, %v", accounts, err)
	}
}

func TestSetKeyTakesHandEnteredModelsWithoutDiscovery(t *testing.T) {
	proxy := &keyProxy{}
	c, _ := keyConnections(t, proxy)
	if _, err := c.SetKey(context.Background(), contract.AccountsSetKeyRequest{DisplayName: "Acme", BaseURL: "https://models.example/v1", Label: "Local", Key: "invented-key", Models: []string{" one ", "two"}}); err != nil {
		t.Fatal(err)
	}
	if models, _ := proxy.body["models"].([]any); len(models) != 2 || models[0] != "one" || proxy.body["discover"] == true {
		t.Fatalf("proxy body = %#v", proxy.body)
	}
}

func TestSetKeyReachesAnExistingCustomProvider(t *testing.T) {
	proxy := &keyProxy{credentials: `{"id":"a","auth_index":"a","provider":"openai-compatible-custom-acme","state":"connected","display_name":"Acme","base_url":"https://models.example/v1","models":["custom-acme/one"]}`}
	c, _ := keyConnections(t, proxy)
	ctx := context.Background()
	if _, err := c.SetKey(ctx, contract.AccountsSetKeyRequest{Provider: "custom-acme", Label: "Second", Key: "invented-key"}); err != nil {
		t.Fatal(err)
	}
	if models, _ := proxy.body["models"].([]any); len(models) != 1 || models[0] != "one" || proxy.body["baseUrl"] != "https://models.example/v1" {
		t.Fatalf("proxy body = %#v", proxy.body)
	}
	proxy.body = nil
	if _, err := c.SetKey(ctx, contract.AccountsSetKeyRequest{DisplayName: "Acme", BaseURL: "https://elsewhere.example/v1", Label: "Second", Key: "invented-key"}); err == nil || proxy.body != nil {
		t.Fatal("a new provider replaced one with the same name")
	}
	if _, err := c.SetKey(ctx, contract.AccountsSetKeyRequest{Provider: "custom-gone", Label: "Second", Key: "invented-key"}); err == nil || proxy.body != nil {
		t.Fatal("a key reached a provider that does not exist")
	}
}

func TestSetKeyReportsAProviderWithNoModels(t *testing.T) {
	proxy := &keyProxy{status: http.StatusUnprocessableEntity}
	c, st := keyConnections(t, proxy)
	ctx := context.Background()
	if _, err := c.SetKey(ctx, contract.AccountsSetKeyRequest{DisplayName: "Acme", BaseURL: "https://models.example/v1", Label: "Local", Key: "invented-key"}); !errors.Is(err, ErrModelsUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if accounts, err := st.Accounts(ctx); err != nil || len(accounts) != 0 {
		t.Fatalf("accounts = %#v, %v", accounts, err)
	}
}

func TestSetKeySendsPresetsThroughTheSamePath(t *testing.T) {
	proxy := &keyProxy{}
	c, _ := keyConnections(t, proxy)
	if _, err := c.SetKey(context.Background(), contract.AccountsSetKeyRequest{Provider: contract.ProviderDeepSeek, Label: "Key", Key: "invented-key"}); err != nil {
		t.Fatal(err)
	}
	if proxy.body["provider"] != "deepseek" || proxy.body["baseUrl"] != "https://api.deepseek.com/v1" || proxy.body["discover"] != true {
		t.Fatalf("proxy body = %#v", proxy.body)
	}
}

func TestRouteAllowsACustomProviderModel(t *testing.T) {
	seen := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.URL.Path }))
	defer server.Close()
	route := handler(&http.Client{Transport: quotaTestTransport{server.URL, http.DefaultTransport}}, "secret", "session", []string{"custom-acme/one"}, []string{"credential"})
	response := httptest.NewRecorder()
	route.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/v1/responses", strings.NewReader(`{"model":"custom-acme/one"}`)))
	if response.Code != http.StatusOK || seen != "/v1/responses" {
		t.Fatalf("code = %d, path = %q", response.Code, seen)
	}
}

func TestProxyConfigHasNoProviderCatalog(t *testing.T) {
	proxy, err := NewProxy(t.TempDir(), "invented-management-key")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Listener.Close()
	data, err := os.ReadFile(proxy.Config)
	if err != nil {
		t.Fatal(err)
	}
	config := string(data)
	if !strings.Contains(config, "force-model-prefix: true") || strings.Contains(config, "z.ai") || strings.Contains(config, "deepseek") {
		t.Fatalf("config:\n%s", config)
	}
}
