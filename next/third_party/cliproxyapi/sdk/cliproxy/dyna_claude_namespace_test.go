package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestDynaClaudeNamespaceRegistryIsolation(t *testing.T) {
	service := &Service{cfg: &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}}}
	registry := GlobalModelRegistry()
	claude := &coreauth.Auth{ID: "dyna-claude-registry", Provider: "claude", Prefix: "plan_anthropic", Status: coreauth.StatusActive}
	other := &coreauth.Auth{ID: "dyna-other-registry", Provider: "claude", Prefix: "other", Status: coreauth.StatusActive}
	for _, auth := range []*coreauth.Auth{claude, other} {
		service.registerModelsForAuth(t.Context(), auth)
		t.Cleanup(func() { registry.UnregisterClient(auth.ID) })
	}
	const model = "claude-sonnet-4-6"
	has := func(client, id string) bool {
		for _, model := range registry.GetModelsForClient(client) {
			if model.ID == id {
				return true
			}
		}
		return false
	}
	if !has(claude.ID, "plan_anthropic/"+model) || has(other.ID, "plan_anthropic/"+model) {
		t.Fatal("plan namespace did not resolve exclusively to its Claude credential")
	}
	if has(claude.ID, model) || has(other.ID, model) || len(registry.GetModelProviders(model)) != 0 {
		t.Fatal("bare Claude model remained routable")
	}
}
