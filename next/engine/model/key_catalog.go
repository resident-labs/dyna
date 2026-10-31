package model

import "github.com/resident-labs/dyna/contract"

// KeyProvider describes a fixed OpenAI-compatible upstream and its models.
type KeyProvider struct {
	Provider contract.AccountProvider
	URL      string
	Models   []string
}

// KeyProviders is the supported key catalog. Endpoints are provider documentation URLs.
func KeyProviders() []KeyProvider {
	return []KeyProvider{
		{contract.ProviderZAI, "https://api.z.ai/api/paas/v4", []string{"glm-5.3", "glm-5.3-flash"}},
		{contract.ProviderOpencodeGo, "https://opencode.ai/inference/go/openai/v1", []string{"glm-5.3", "kimi-k3", "minimax-m3.1-flash"}},
		{contract.ProviderDeepSeek, "https://api.deepseek.com/v1", []string{"deepseek-chat", "deepseek-reasoner"}},
	}
}
