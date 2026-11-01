package model

import "github.com/resident-labs/dyna/contract"

var oauthProviders = []struct {
	info  contract.AccountProviderInfo
	proxy string
}{
	{contract.AccountProviderInfo{ID: contract.ProviderAnthropic, DisplayName: "Claude", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowCallback}, "claude"},
	{contract.AccountProviderInfo{ID: contract.ProviderOpenAI, DisplayName: "ChatGPT", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowCallback}, "codex"},
	{contract.AccountProviderInfo{ID: contract.ProviderKimi, DisplayName: "Kimi Code", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowDevice}, "kimi"},
	{contract.AccountProviderInfo{ID: contract.ProviderXAI, DisplayName: "xAI", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowDevice}, "xai"},
}

// OAuthProxyProvider returns the proxy's name for a provider with OAuth sign-in.
func OAuthProxyProvider(id contract.AccountProvider) (string, bool) {
	for _, provider := range oauthProviders {
		if provider.info.ID == id {
			return provider.proxy, true
		}
	}
	return "", false
}

// AccountProviders describes the same catalog used by the connection handlers.
func AccountProviders() []contract.AccountProviderInfo {
	providers := make([]contract.AccountProviderInfo, 0, len(oauthProviders)+len(KeyProviders()))
	for _, provider := range oauthProviders {
		providers = append(providers, provider.info)
	}
	for _, provider := range KeyProviders() {
		providers = append(providers, contract.AccountProviderInfo{ID: provider.Provider, DisplayName: provider.DisplayName, ConnectMethod: contract.ConnectKey, ConnectFlow: contract.ConnectFlowKey})
	}
	return providers
}
