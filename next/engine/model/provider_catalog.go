package model

import (
	"strings"

	"github.com/resident-labs/dyna/contract"
)

var oauthProviders = []struct {
	info  contract.AccountProviderInfo
	proxy string
}{
	{contract.AccountProviderInfo{ID: contract.ProviderAnthropic, DisplayName: "Claude", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowCallback}, "claude"},
	{contract.AccountProviderInfo{ID: contract.ProviderOpenAI, DisplayName: "ChatGPT", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowCallback}, "codex"},
	{contract.AccountProviderInfo{ID: contract.ProviderKimi, DisplayName: "Kimi Code", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowDevice}, "kimi"},
	{contract.AccountProviderInfo{ID: contract.ProviderXAI, DisplayName: "xAI", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowDevice}, "xai"},
	{contract.AccountProviderInfo{ID: contract.ProviderMeta, DisplayName: "Meta", ConnectMethod: contract.ConnectOAuth, ConnectFlow: contract.ConnectFlowDevice, AlsoKey: true}, "meta"},
}

// CleanAccountLabel trims a label and reports whether an account may carry it.
func CleanAccountLabel(label string) (string, bool) {
	label = strings.TrimSpace(label)
	return label, label != "" && len(label) <= 128 && !strings.ContainsAny(label, "\r\n@")
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

// AccountProviders describes the built-in catalog and the providers saved credentials add.
func AccountProviders(credentials []Credential) []contract.AccountProviderInfo {
	providers := make([]contract.AccountProviderInfo, 0, len(oauthProviders)+len(KeyPresets()))
	for _, provider := range oauthProviders {
		providers = append(providers, provider.info)
	}
	for _, preset := range KeyPresets() {
		providers = append(providers, contract.AccountProviderInfo{ID: preset.Provider, DisplayName: preset.DisplayName, ConnectMethod: contract.ConnectKey, ConnectFlow: contract.ConnectFlowKey})
	}
	return append(providers, customProviders(credentials)...)
}
