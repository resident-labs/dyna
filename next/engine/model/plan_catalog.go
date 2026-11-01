package model

import (
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
)

// The proxy Kimi executor translates these exact names to Pi's upstream IDs.
var planAliases = map[contract.AccountProvider]map[string]string{
	contract.ProviderKimiCoding: {
		"k3":                        "kimi-k3",
		"k3-256k":                   "kimi-k3-256k",
		"kimi-for-coding":           "kimi-k2.8-code",
		"kimi-for-coding-highspeed": "kimi-k2.7-code-highspeed",
	},
}

func planProxyModel(provider contract.AccountProvider, id string) string {
	if alias := planAliases[provider][id]; alias != "" {
		return alias
	}
	return id
}

func proxyModel(namespace, id string) string {
	if provider, ok := strings.CutPrefix(namespace, "plan_"); ok {
		return planProxyModel(contract.AccountProvider(provider), id)
	}
	return id
}

// SetPlanModels intersects Pi's pinned list with the proxy's immutable plan lists.
func (p *Proxy) SetPlanModels(c Catalog) error {
	pinned, err := cliproxy.DynaPinnedModels()
	if err != nil {
		return err
	}
	available := map[contract.AccountProvider][]string{
		contract.ProviderAnthropic:  pinned.Claude,
		contract.ProviderOpenAI:     pinned.Codex,
		contract.ProviderKimiCoding: pinned.Kimi,
		contract.ProviderXAI:        pinned.XAI,
	}
	p.planModels = make(map[contract.AccountProvider][]contract.CatalogModel)
	p.planDefaults = make(map[contract.AccountProvider]string)
	for _, provider := range c.Providers {
		id := contract.AccountProvider(provider.ID)
		ids, ok := available[id]
		if !ok {
			continue
		}
		set := make(map[string]bool, len(ids))
		for _, name := range ids {
			set[name] = true
		}
		activation := set
		if id == contract.ProviderOpenAI {
			activation = make(map[string]bool, len(pinned.CodexCommon))
			for _, name := range pinned.CodexCommon {
				activation[name] = true
			}
		}
		for _, model := range provider.Models {
			if !set[planProxyModel(id, model.ID)] {
				continue
			}
			p.planModels[id] = append(p.planModels[id], contract.CatalogModel{ID: model.ID, Name: model.Name})
			if activation[planProxyModel(id, model.ID)] && (p.planDefaults[id] == "" || model.ID == provider.DefaultModel) {
				p.planDefaults[id] = model.ID
			}
		}
		if p.planDefaults[id] == "" {
			delete(p.planModels, id)
		}
	}
	return nil
}
