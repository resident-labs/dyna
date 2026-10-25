package runtime

import (
	"strings"

	"dyna-agent/internal/account/store"
)

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
}

// providerForModel returns the provider an account is selected for.
//
// Pi runs every provider, so its model names the provider: a Pi turn of
// "zai/glm-5.3" needs the zai account. Another harness serves one provider and
// keeps its own credentials for it (Codex takes its keys from the owner's codex
// home), so its model does not choose an account and the harness does.
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

// selectionProvider names the provider whose selection a harness uses. Pi
// keeps one selection per provider. Another harness serves its own provider
// and keeps one selection, whatever provider its accounts' credentials name.
func selectionProvider(h string, p store.Provider) store.Provider {
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
		return n.Provider == p && p != ""
	}
	return true
}
