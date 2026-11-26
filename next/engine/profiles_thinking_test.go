package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestLoadProfilesThinkingMax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"luna":{"provider":"openai","model":"gpt-6-luna","allowedModels":["gpt-6-luna"],"thinking":"max"}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := profiles.List()[0].Thinking; got != contract.ThinkingMax {
		t.Fatalf("thinking = %q, want max", got)
	}
}

// A live profiles.json that still names an antigravity/ model must not stop the engine: it loads, and an edit is refused.
func TestLoadProfilesKeepsALeftoverAntigravityProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"neon":{"provider":"anthropic","model":"antigravity/gemini-4-neon-xhigh","allowedModels":["antigravity/gemini-4-neon-xhigh"],"thinking":"xhigh"}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(path); err != nil {
		t.Fatal(err)
	}
}

func TestValidateProfilesRejectsUnknownThinking(t *testing.T) {
	err := validateProfiles(map[contract.ProfileName]profile{"luna": {Provider: contract.ModelProviderOpenAI, Model: "gpt-6-luna", AllowedModels: []string{"gpt-6-luna"}, Thinking: "unknown"}})
	if err == nil {
		t.Fatal("accepted unknown thinking level")
	}
}
