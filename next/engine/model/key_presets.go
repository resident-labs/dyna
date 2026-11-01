package model

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// MetaKeyModel proves a Meta key reached the proxy's own Meta catalog. Meta keys run
// on that native executor, so they are not an OpenAI-compatible key provider.
const MetaKeyModel = "muse-wonder-full-1.0"

const (
	// CustomProviderPrefix namespaces providers the owner adds, so no name can
	// become anthropic, zai or another built-in provider.
	CustomProviderPrefix = "custom-"
	proxyCompatPrefix    = "openai-compatible-"
)

// KeyPreset seeds an ordinary OpenAI-compatible key provider. Its ID is stable, so the
// proxy can key special handling such as usage readers by it. URL is authoritative for
// saved accounts, and Models covers a provider that does not list its own.
type KeyPreset struct {
	Provider    contract.AccountProvider
	DisplayName string
	URL         string
	Models      []string
}

// KeyPresets are provider documentation URLs.
func KeyPresets() []KeyPreset {
	return []KeyPreset{
		// The coding endpoint: z.ai bills the general /api/paas/v4 to account balance,
		// even for a key with plan quota left (docs.z.ai/devpack/notice/api-key-management).
		{contract.ProviderZAI, "z.ai", "https://api.z.ai/api/coding/paas/v4", []string{"glm-5.3", "glm-5.3-flash"}},
		{contract.ProviderOpencodeGo, "OpenCode Go", "https://opencode.ai/inference/go/v1", []string{"glm-5.3", "kimi-k3", "minimax-m3.1-flash"}},
		{contract.ProviderDeepSeek, "DeepSeek", "https://api.deepseek.com/v1", []string{"deepseek-chat", "deepseek-reasoner"}},
	}
}

// PresetForProxy finds the preset of a credential's proxy provider.
func PresetForProxy(proxyProvider string) (KeyPreset, bool) {
	for _, preset := range KeyPresets() {
		if proxyProvider == proxyCompatPrefix+string(preset.Provider) {
			return preset, true
		}
	}
	return KeyPreset{}, false
}

// IsCustomProxyProvider says whether a proxy provider is one the owner added.
func IsCustomProxyProvider(proxyProvider string) bool {
	return strings.HasPrefix(proxyProvider, proxyCompatPrefix+CustomProviderPrefix)
}

// customProviders lists the providers owner-added credentials describe. The proxy
// holds them, so a provider exists while it has an account.
func customProviders(credentials []Credential) []contract.AccountProviderInfo {
	var providers []contract.AccountProviderInfo
	for _, credential := range credentials {
		if !IsCustomProxyProvider(credential.Provider) {
			continue
		}
		id := contract.AccountProvider(strings.TrimPrefix(credential.Provider, proxyCompatPrefix))
		if !slices.ContainsFunc(providers, func(p contract.AccountProviderInfo) bool { return p.ID == id }) {
			providers = append(providers, contract.AccountProviderInfo{ID: id, DisplayName: credential.DisplayName, ConnectMethod: contract.ConnectKey, ConnectFlow: contract.ConnectFlowKey})
		}
	}
	slices.SortFunc(providers, func(a, b contract.AccountProviderInfo) int { return cmp.Compare(a.DisplayName, b.DisplayName) })
	return providers
}

// customProviderID keeps ASCII letters and digits. A name with none uses its hash.
func customProviderID(name string) contract.AccountProvider {
	slug := strings.Trim(strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		default:
			return '-'
		}
	}, name), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	if slug == "" {
		sum := sha256.Sum256([]byte(name))
		slug = hex.EncodeToString(sum[:8])
	}
	return contract.AccountProvider(CustomProviderPrefix + slug)
}

// ValidNewProvider checks an owner-added provider. The address only has to parse: the
// owner may point it at a local model server. A built-in's name is refused, as two
// providers with one name could not be told apart.
func ValidNewProvider(name, baseURL string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "\r\n") {
		return false
	}
	for _, provider := range AccountProviders(nil) {
		if strings.EqualFold(name, provider.DisplayName) || strings.EqualFold(name, string(provider.ID)) {
			return false
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// cleanModels trims hand-entered IDs and drops repeats and any the proxy could not list.
func cleanModels(models []string) []string {
	var clean []string
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model != "" && !strings.ContainsAny(model, ", \t\r\n") && !slices.Contains(clean, model) {
			clean = append(clean, model)
		}
	}
	return clean
}
