package model

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func testPools() map[string]pool {
	out := make(map[string]pool)
	for _, option := range oauthProviders {
		info := option.info
		info.Models = []contract.CatalogModel{{ID: "model"}}
		out["plan_"+string(info.ID)] = makePool(info, option.proxy, "model")
	}
	return out
}
func testKeyPools() map[string]pool {
	out := make(map[string]pool)
	for _, key := range testKeyProviders {
		out[string(key.Provider)] = makePool(contract.AccountProviderInfo{ID: key.Provider, Models: key.Models}, "", key.DefaultModel)
	}
	return out
}

func TestPlanCatalogUsesDefaultAndExactAliases(t *testing.T) {
	p := &Proxy{}
	catalog := Catalog{Plans: []PlanProvider{
		{ID: "anthropic", DefaultModel: "claude-opus-4-6", Models: []PlanModel{{ID: "not-served"}, {ID: "claude-sonnet-4-6"}, {ID: "claude-opus-4-6"}}},
		{ID: "kimi-coding", DefaultModel: "kimi-for-coding", Models: []PlanModel{{ID: "k3", ProxyID: "kimi-k3"}, {ID: "kimi-for-coding", ProxyID: "kimi-k2.8-code"}}},
		{ID: "openai", DefaultModel: "gpt-5.6-sol", Models: []PlanModel{{ID: "gpt-5.6-sol"}, {ID: "gpt-5.6-luna"}}},
	}}
	if err := p.initPools(catalog); err != nil {
		t.Fatal(err)
	}
	if len(p.pools["plan_anthropic"].info.Models) != 2 || p.pools["plan_anthropic"].defaultModel != "claude-opus-4-6" {
		t.Fatal("wrong Claude plan models or default")
	}
	if len(p.pools["plan_kimi-coding"].info.Models) != 2 || p.pools["plan_kimi-coding"].defaultModel != "kimi-for-coding" || p.pools["plan_kimi-coding"].modelID("kimi-for-coding") != "kimi-k2.8-code" {
		t.Fatal("wrong Kimi plan models or aliases")
	}
	if len(p.pools["plan_openai"].info.Models) != 2 || p.pools["plan_openai"].defaultModel != "gpt-5.6-luna" {
		t.Fatal("paid-only model was hidden or selected for activation")
	}
	if len(p.pools["plan_xai"].info.Models) != 0 {
		t.Fatal("offered provider without models")
	}
}

func TestDualProviderPublishesBothPools(t *testing.T) {
	p := &Proxy{}
	catalog := Catalog{Plans: []PlanProvider{{ID: "anthropic", Models: []PlanModel{{ID: "claude-sonnet-4-6"}, {ID: "claude-opus-4-6"}}}}, Keys: []KeyProvider{{Provider: "anthropic", DefaultModel: "key-only", Models: []contract.CatalogModel{{ID: "key-only"}, {ID: "claude-sonnet-4-6"}}}}}
	if err := p.initPools(catalog); err != nil {
		t.Fatal(err)
	}
	info := p.AccountProviders()[0]
	if len(p.pools["plan_anthropic"].info.Models) != 2 {
		t.Fatal("plan pool includes key-only model")
	}
	if len(info.Models) != 3 {
		t.Fatalf("published models: %+v", info.Models)
	}
	for _, model := range info.Models {
		want := []contract.ModelBilling{contract.ModelBillingKey}
		if model.ID == "claude-sonnet-4-6" {
			want = []contract.ModelBilling{contract.ModelBillingPlan, contract.ModelBillingKey}
		}
		if model.ID == "claude-opus-4-6" {
			want = []contract.ModelBilling{contract.ModelBillingPlan}
		}
		if !reflect.DeepEqual(model.Billing, want) {
			t.Fatalf("%s billing = %v, want %v", model.ID, model.Billing, want)
		}
	}
	if !reflect.DeepEqual(info, p.AccountProviders()[0]) {
		t.Fatal("catalog changed between reads")
	}
	if _, err := p.Namespace(contract.ModelChoice{Provider: "anthropic", Billing: contract.ModelBillingPlan, ID: "key-only"}, nil); err == nil {
		t.Fatal("plan accepted a key-only model")
	}
	if _, err := p.Namespace(contract.ModelChoice{Provider: "anthropic", Billing: contract.ModelBillingKey, ID: "key-only"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedPlanCatalogDrift(t *testing.T) {
	path := os.Getenv("DYNA_PI_TARBALL")
	if path == "" {
		t.Skip("set DYNA_PI_TARBALL to the packed Pi artifact")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	catalog, err := LoadCatalog(data)
	t.Logf("pinned catalog load: %s", time.Since(start))
	if err != nil {
		t.Fatal(err)
	}
	keys := catalog.Keys
	p, err := NewProxy(t.TempDir(), "fixture", catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Listener.Close()
	cfg, err := config.LoadConfig(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		var registered []config.OpenAICompatibilityModel
		for _, entry := range cfg.OpenAICompatibility {
			if entry.Name == string(key.Provider) {
				registered = entry.Models
				break
			}
		}
		for _, model := range key.Models {
			found := false
			for _, item := range registered {
				if item.Name == model.ID && item.Alias == model.ID {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s/%s has no proxy key registration", key.Provider, model.ID)
			}
		}
	}
	pinned, err := cliproxy.DynaPinnedModels()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[contract.AccountProvider][]string{
		contract.ProviderAnthropic: pinned.Claude, contract.ProviderOpenAI: append(append(append(append([]string{}, pinned.CodexFree...), pinned.CodexTeam...), pinned.CodexPlus...), pinned.CodexPro...),
		contract.ProviderKimiCoding: pinned.Kimi, contract.ProviderXAI: pinned.XAI,
	}
	for _, provider := range oauthProviders {
		id := provider.info.ID
		defaultID := ""
		for _, piProvider := range catalog.Plans {
			if piProvider.ID == id {
				defaultID = piProvider.DefaultModel
				break
			}
		}
		if defaultID == "" {
			t.Errorf("%s has no packed Pi default", id)
		}
		if len(p.pools["plan_"+string(id)].info.Models) == 0 {
			t.Errorf("no pinned plan models for %s", id)
			continue
		}
		set := make(map[string]bool)
		for _, name := range ids[id] {
			set[name] = true
		}
		for _, model := range p.pools["plan_"+string(id)].info.Models {
			if !p.pools["plan_"+string(id)].models[model.ID] {
				continue
			}
			if !set[p.pools["plan_"+string(id)].modelID(model.ID)] {
				t.Errorf("%s/%s has no compiled proxy model", id, model.ID)
			}
		}
		common := set
		if id == contract.ProviderOpenAI {
			common = make(map[string]bool)
			for _, name := range pinned.CodexFree {
				common[name] = true
			}
			for _, tier := range [][]string{pinned.CodexTeam, pinned.CodexPlus, pinned.CodexPro} {
				set := map[string]bool{}
				for _, name := range tier {
					set[name] = true
				}
				for name := range common {
					if !set[name] {
						delete(common, name)
					}
				}
			}
		}
		expected := ""
		for _, model := range p.pools["plan_"+string(id)].info.Models {
			if p.pools["plan_"+string(id)].models[model.ID] && common[p.pools["plan_"+string(id)].modelID(model.ID)] && (expected == "" || model.ID == defaultID) {
				expected = model.ID
			}
		}
		if p.pools["plan_"+string(id)].defaultModel != expected {
			t.Errorf("%s activation model = %q, want %q", id, p.pools["plan_"+string(id)].defaultModel, expected)
		}
	}
}
