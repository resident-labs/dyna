package model

import "github.com/resident-labs/dyna/contract"

// KeyProvider describes a fixed OpenAI-compatible upstream and its models.
type KeyProvider struct {
	Provider    contract.AccountProvider
	DisplayName string
	URL         string
	Models      []string
}

// MetaKeyModel proves a Meta key reached the proxy's own Meta catalog. Meta keys run
// on that native executor, not as an OpenAI-compatible upstream, so they stay out of
// KeyProviders.
const MetaKeyModel = "muse-wonder-full-1.0"

// KeyProviders is the supported key catalog. Endpoints are provider documentation URLs.
func KeyProviders() []KeyProvider {
	return []KeyProvider{
		// The coding endpoint: z.ai bills the general /api/paas/v4 to account balance,
		// even for a key with plan quota left (docs.z.ai/devpack/notice/api-key-management).
		{contract.ProviderZAI, "z.ai", "https://api.z.ai/api/coding/paas/v4", []string{"glm-5.3", "glm-5.3-flash"}},
		{contract.ProviderOpencodeGo, "OpenCode Go", "https://opencode.ai/inference/go/v1", []string{"glm-5.3", "kimi-k3", "minimax-m3.1-flash"}},
		{contract.ProviderDeepSeek, "DeepSeek", "https://api.deepseek.com/v1", []string{"deepseek-chat", "deepseek-reasoner"}},
	}
}
