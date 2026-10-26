package runtime

import (
	"errors"
	"strings"

	"dyna-agent/internal/account/store"
)

// ErrPiAnthropicOAuth refuses an Anthropic OAuth sign-in through Pi. Anthropic
// bills a Claude Pro/Max sign-in used by a third-party harness as extra
// per-token usage, so only Claude Code or an Anthropic API key may run it.
var ErrPiAnthropicOAuth = errors.New("Anthropic Claude Pro/Max sign-ins cannot run through Pi. Use the Claude Code harness, or add an Anthropic API key in Pi.")

// piProviderIDs maps the provider IDs Pi stores in its login file, and the
// model prefixes it uses, to the provider an account belongs to. Pi names a
// provider in a model as "<provider>/<model>", so the same table serves both.
var piProviderIDs = map[string]store.Provider{
	"anthropic":    store.ProviderAnthropic,
	"openai":       store.ProviderChatGPT,
	"openai-codex": store.ProviderChatGPT,
	"chatgpt":      store.ProviderChatGPT,
	"kimi":         store.ProviderKimiCode,
	"kimi-coding":  store.ProviderKimiCode,
	"xai":          store.ProviderXAI,
	"meta":         store.ProviderMetaMuse,
	"meta-muse":    store.ProviderMetaMuse,
	"muse":         store.ProviderMetaMuse,
	"opencode":     store.ProviderOpenCodeGo,
	"opencode-go":  store.ProviderOpenCodeGo,
	"deepseek":     store.ProviderDeepSeek,
	"zai":          store.ProviderZAI,
	"brave":        store.ProviderBrave,
	"exa":          store.ProviderExa,
	"tavily":       store.ProviderTavily,
}

// providerForModel returns the provider an account is selected for.
//
// Pi runs every provider, so its model names the provider: a Pi turn of
// "zai/glm-5.3" needs the zai account. Another harness serves one provider and
// keeps its own credentials for it, so its model does not choose an account and
// the harness does.
func providerForModel(harness, model string) store.Provider {
	if harness == "pi" {
		if prefix, _, found := strings.Cut(model, "/"); found && prefix != "" {
			return piProvider(prefix)
		}
		return ""
	}
	provider, _ := store.ProviderFor(harness, "")
	return provider
}

// ProviderForModel names the provider a profile model runs on.
func ProviderForModel(harness, model string) store.Provider { return providerForModel(harness, model) }

// piProvider returns the provider a Pi provider ID names. An ID this release
// does not know keeps its own name, so a newer provider needs no code change.
func piProvider(id string) store.Provider {
	if provider, ok := piProviderIDs[id]; ok {
		return provider
	}
	return store.Provider(id)
}

// piProviderName maps each provider to the ID Pi uses for its login entry and
// its model prefixes.
var piProviderName = map[store.Provider]string{
	store.ProviderAnthropic:  "anthropic",
	store.ProviderChatGPT:    "openai",
	store.ProviderKimiCode:   "kimi-coding",
	store.ProviderXAI:        "xai",
	store.ProviderMetaMuse:   "meta",
	store.ProviderOpenCodeGo: "opencode",
	store.ProviderDeepSeek:   "deepseek",
	store.ProviderZAI:        "zai",
	store.ProviderBrave:      "brave",
	store.ProviderExa:        "exa",
	store.ProviderTavily:     "tavily",
}

// PiProviderID returns the Pi provider ID of p, so a key goes to that
// provider's own entry.
func PiProviderID(p store.Provider) (string, bool) {
	id, ok := piProviderName[p]
	return id, ok
}

// piOAuthProviderName maps each provider to the ID Pi's OAuth login uses.
// ChatGPT signs in through openai-codex, not the openai API-key provider.
var piOAuthProviderName = map[store.Provider]string{
	store.ProviderAnthropic: "anthropic",
	store.ProviderChatGPT:   "openai-codex",
	store.ProviderKimiCode:  "kimi-coding",
	store.ProviderXAI:       "xai",
	store.ProviderMetaMuse:  "meta",
}

// PiOAuthProviderID returns the Pi OAuth provider ID of p, or false when Pi
// has no OAuth sign-in for that provider.
func PiOAuthProviderID(p store.Provider) (string, bool) {
	id, ok := piOAuthProviderName[p]
	return id, ok
}

// PiProvider names the provider a Pi provider ID or a Dyna provider name means.
// An ID this release does not know is not a provider Dyna can add a key for.
func PiProvider(name string) (store.Provider, bool) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "", false
	}
	provider := piProvider(name)
	if _, known := piProviderName[provider]; !known {
		return "", false
	}
	return provider, true
}

// SelectionProvider names the provider whose selection a harness uses. Pi
// keeps one selection per provider. Another harness serves its own provider
// and keeps one selection, whatever provider its accounts' credentials name.
func SelectionProvider(h string, p store.Provider) store.Provider {
	if h == "pi" {
		return p
	}
	return accountProvider(h)
}

// accountRuns reports whether account n can launch a turn of harness h for
// provider p. A harness other than Pi serves one provider and keeps one
// selection, so any of its accounts serves it, whatever provider the account's
// credential names. Pi reads one login per provider, so it needs the account of
// that provider.
func accountRuns(h string, n NativeAccount, p store.Provider) bool {
	if n.Harness != h {
		return false
	}
	if h == "pi" {
		if n.Provider != p || p == "" {
			return false
		}
		// A Claude Pro/Max OAuth entry in a Pi home is never used for a turn.
		if p == store.ProviderAnthropic && n.Kind != store.KindAPIKey {
			return false
		}
		return true
	}
	return true
}
