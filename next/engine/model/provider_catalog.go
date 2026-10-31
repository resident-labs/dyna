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
	providers := make([]contract.AccountProviderInfo, 0, len(oauthProviders)+len(p.keyProviders))
	for _, provider := range oauthProviders {
		info := provider.info
		if len(p.planModels[info.ID]) == 0 {
			continue
		}
		info.Models = p.planModels[info.ID]
		providers = append(providers, info)
	}
	for _, provider := range p.keyProviders {
		index := -1
		for i := range providers {
			if providers[i].ID == provider.Provider {
				index = i
				break
			}
		}
		if index < 0 {
			providers = append(providers, contract.AccountProviderInfo{ID: provider.Provider, DisplayName: provider.DisplayName})
			index = len(providers) - 1
		}
		info := &providers[index]
		info.Flows = append(info.Flows, contract.ConnectFlowKey)
		info.KeyName = provider.KeyName
		info.Models = provider.Models
	}
	// Legacy shim: remove in catalog PR 4.
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
