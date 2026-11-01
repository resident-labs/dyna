package model

import (
	"os"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func testPlanModels() map[contract.AccountProvider][]contract.CatalogModel {
	out := make(map[contract.AccountProvider][]contract.CatalogModel)
	for _, entry := range oauthProviders {
		out[entry.info.ID] = []contract.CatalogModel{{ID: "model"}}
	}
	return out
}

func TestPlanCatalogUsesDefaultAndExactAliases(t *testing.T) {
	p := &Proxy{}
	catalog := Catalog{Providers: []CatalogProvider{
		{ID: "anthropic", DefaultModel: "claude-opus-4-6", Models: []CatalogModel{{ID: "not-served"}, {ID: "claude-sonnet-4-6"}, {ID: "claude-opus-4-6"}}},
		{ID: "kimi-coding", DefaultModel: "kimi-for-coding", Models: []CatalogModel{{ID: "k3"}, {ID: "kimi-for-coding"}}},
		{ID: "openai", DefaultModel: "gpt-5.6-sol", Models: []CatalogModel{{ID: "gpt-5.6-sol"}, {ID: "gpt-5.6-luna"}}},
	}}
	if err := p.SetPlanModels(catalog); err != nil {
		t.Fatal(err)
	}
	if len(p.planModels[contract.ProviderAnthropic]) != 2 || p.planDefaults[contract.ProviderAnthropic] != "claude-opus-4-6" {
		t.Fatal("wrong Claude plan models or default")
	}
	if len(p.planModels[contract.ProviderKimiCoding]) != 2 || p.planDefaults[contract.ProviderKimiCoding] != "kimi-for-coding" || planProxyModel(contract.ProviderKimiCoding, "kimi-for-coding") != "kimi-k2.8-code" {
		t.Fatal("wrong Kimi plan models or aliases")
	}
	if len(p.planModels[contract.ProviderOpenAI]) != 2 || p.planDefaults[contract.ProviderOpenAI] != "gpt-5.6-luna" {
		t.Fatal("paid-only model was hidden or selected for activation")
	}
	if len(p.planModels[contract.ProviderXAI]) != 0 {
		t.Fatal("offered provider without models")
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
	catalog, skipped, err := LoadCatalog(data)
	t.Logf("pinned catalog load: %s", time.Since(start))
	if err != nil {
		t.Fatal(err)
	}
	keys := catalog.KeyProviders()
	p, err := NewProxy(t.TempDir(), "fixture", keys...)
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
	if err := p.SetPlanModels(catalog); err != nil {
		t.Fatal(err)
	}
	pinned, err := cliproxy.DynaPinnedModels()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[contract.AccountProvider][]string{
		contract.ProviderAnthropic: pinned.Claude, contract.ProviderOpenAI: pinned.Codex,
		contract.ProviderKimiCoding: pinned.Kimi, contract.ProviderXAI: pinned.XAI,
	}
	for _, provider := range oauthProviders {
		id := provider.info.ID
		defaultID := ""
		for _, piProvider := range catalog.Providers {
			if piProvider.ID == string(id) {
				defaultID = piProvider.DefaultModel
				break
			}
		}
		if defaultID == "" {
			t.Errorf("%s has no packed Pi default", id)
		}
		if len(p.planModels[id]) == 0 {
			t.Errorf("no pinned plan models for %s; skipped=%v", id, skipped)
			continue
		}
		set := make(map[string]bool)
		for _, name := range ids[id] {
			set[name] = true
		}
		for _, model := range p.planModels[id] {
			if !set[planProxyModel(id, model.ID)] {
				t.Errorf("%s/%s has no compiled proxy model", id, model.ID)
			}
		}
		common := set
		if id == contract.ProviderOpenAI {
			common = make(map[string]bool)
			for _, name := range pinned.CodexCommon {
				common[name] = true
			}
		}
		expected := ""
		for _, model := range p.planModels[id] {
			if common[planProxyModel(id, model.ID)] && (expected == "" || model.ID == defaultID) {
				expected = model.ID
			}
		}
		if p.planDefaults[id] != expected {
			t.Errorf("%s activation model = %q, want %q", id, p.planDefaults[id], expected)
		}
	}
}
