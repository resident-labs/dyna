package model

import "github.com/resident-labs/dyna/contract"

var oauthProviders = []struct {
	info  contract.AccountProviderInfo
	proxy string
}{
	{contract.AccountProviderInfo{ID: contract.ProviderAnthropic, DisplayName: "Claude", Flows: []contract.ConnectFlow{contract.ConnectFlowCallback}}, "claude"},
	{contract.AccountProviderInfo{ID: contract.ProviderOpenAI, DisplayName: "ChatGPT", Flows: []contract.ConnectFlow{contract.ConnectFlowCallback}}, "codex"},
	{contract.AccountProviderInfo{ID: contract.ProviderKimiCoding, DisplayName: "Kimi Code", Flows: []contract.ConnectFlow{contract.ConnectFlowDevice}}, "kimi"},
	{contract.AccountProviderInfo{ID: contract.ProviderXAI, DisplayName: "xAI", Flows: []contract.ConnectFlow{contract.ConnectFlowDevice}}, "xai"},
}

// AccountProviders describes the same catalog used by the connection handlers.
func (p *Proxy) AccountProviders() []contract.AccountProviderInfo {
	var providers []contract.AccountProviderInfo
	indices := make(map[contract.AccountProvider]int)
	for _, option := range oauthProviders {
		entry, ok := p.pools["plan_"+string(option.info.ID)]
		if !ok {
			continue
		}
		indices[entry.info.ID] = len(providers)
		providers = append(providers, entry.info)
	}
	for _, namespace := range p.keyOrder {
		info := p.pools[namespace].info
		if index, ok := indices[info.ID]; ok {
			providers[index].Flows = append(providers[index].Flows, contract.ConnectFlowKey)
			providers[index].KeyName = info.KeyName
			plan := p.pools["plan_"+namespace]
			for _, model := range info.Models {
				if !plan.models[model.ID] {
					providers[index].Models = append(providers[index].Models, model)
				}
			}
		} else {
			providers = append(providers, info)
		}
	}
	// TODO: legacy/shim remove later — connectMethod and connectFlow; catalog PR 4 removes them.
	for i := range providers {
		providers[i].ConnectFlow = providers[i].Flows[0]
		providers[i].ConnectMethod = contract.ConnectOAuth
		if providers[i].Flows[0] == contract.ConnectFlowKey {
			providers[i].ConnectMethod = contract.ConnectKey
		}
	}
	return providers
}

func (c *Connections) AccountProviders() []contract.AccountProviderInfo {
	return c.proxy.AccountProviders()
}
