package model

import "github.com/resident-labs/dyna/contract"

// KeyProvider describes a fixed OpenAI-compatible upstream and its models.
type KeyProvider struct {
	Provider    contract.AccountProvider
	DisplayName string
	URL         string
	Models      []string
}

// KeyProviders is the supported key catalog. Endpoints are provider documentation URLs.
func KeyProviders() []KeyProvider {
	return []KeyProvider{
		{contract.ProviderZAI, "z.ai", "https://api.z.ai/api/paas/v4", []string{"glm-5.3", "glm-5.3-flash"}},
		{contract.ProviderOpencodeGo, "OpenCode Go", "https://opencode.ai/inference/go/v1", []string{"glm-5.3", "kimi-k3", "minimax-m3.1-flash"}},
		{contract.ProviderDeepSeek, "DeepSeek", "https://api.deepseek.com/v1", []string{"deepseek-chat", "deepseek-reasoner"}},
	}
}
