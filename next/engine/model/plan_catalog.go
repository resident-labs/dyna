package model

import (
	"github.com/resident-labs/dyna/contract"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
)

type pool struct {
	info         contract.AccountProviderInfo
	proxy        string
	models       map[string]bool
	proxyIDs     map[string]string
	defaultModel string
}

func (p *Proxy) initPools(c Catalog) error {
	pinned, err := cliproxy.DynaPinnedModels()
	if err != nil {
		return err
	}
	p.pools = make(map[string]pool)
	for _, key := range c.Keys {
		p.pools[string(key.Provider)] = makePool(contract.AccountProviderInfo{ID: key.Provider, DisplayName: key.DisplayName, KeyName: key.KeyName, Flows: []contract.ConnectFlow{contract.ConnectFlowKey}, Models: key.Models}, "", key.DefaultModel)
		p.keyOrder = append(p.keyOrder, string(key.Provider))
	}
	for _, provider := range c.Plans {
		var info contract.AccountProviderInfo
		var proxy string
		for _, option := range oauthProviders {
			if option.info.ID == provider.ID {
				info, proxy = option.info, option.proxy
				break
			}
		}
		var tiers [][]string
		switch provider.ID {
		case contract.ProviderAnthropic:
			tiers = [][]string{pinned.Claude}
		case contract.ProviderOpenAI:
			tiers = [][]string{pinned.CodexFree, pinned.CodexTeam, pinned.CodexPlus, pinned.CodexPro}
		case contract.ProviderKimiCoding:
			tiers = [][]string{pinned.Kimi}
		case contract.ProviderXAI:
			tiers = [][]string{pinned.XAI}
		default:
			continue
		}
		counts := make(map[string]int)
		for _, tier := range tiers {
			seen := make(map[string]bool)
			for _, id := range tier {
				if !seen[id] {
					counts[id]++
					seen[id] = true
				}
			}
		}
		var selected string
		proxyIDs := make(map[string]string)
		for _, model := range provider.Models {
			proxyID := model.ProxyID
			if proxyID == "" {
				proxyID = model.ID
			}
			count := counts[proxyID]
			if count == 0 {
				continue
			}
			info.Models = append(info.Models, contract.CatalogModel{ID: model.ID, Name: model.Name})
			proxyIDs[model.ID] = proxyID
			if count == len(tiers) && (selected == "" || model.ID == provider.DefaultModel) {
				selected = model.ID
			}
		}
		if selected != "" {
			entry := makePool(info, proxy, selected)
			entry.proxyIDs = proxyIDs
			p.pools["plan_"+string(provider.ID)] = entry
		}
	}
	return nil
}

func makePool(info contract.AccountProviderInfo, proxy, defaultModel string) pool {
	models := make(map[string]bool, len(info.Models))
	for _, model := range info.Models {
		models[model.ID] = true
	}
	return pool{info: info, proxy: proxy, models: models, defaultModel: defaultModel}
}

func (p pool) modelID(id string) string {
	if mapped := p.proxyIDs[id]; mapped != "" {
		return mapped
	}
	return id
}
