package model

import (
	"os"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
)

func testPlanModels() map[contract.AccountProvider][]contract.CatalogModel {
	out := make(map[contract.AccountProvider][]contract.CatalogModel)
	for _, entry := range oauthProviders {
		out[entry.info.ID] = []contract.CatalogModel{{ID: "model"}}
	}
	return out
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
	p := &Proxy{}
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
		if p.planDefaults[id] == "" {
			t.Errorf("%s has no activation model", id)
		}
	}
}
