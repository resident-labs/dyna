package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLegacyClaudeGenerationKeepsManifest(t *testing.T) {
	m, account, root := editManager(t)
	g, err := m.Generate("claude-code", account, root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(g.Directory, "generation.json")
	var manifest generationManifest
	if err := json.Unmarshal(fileBytes(t, path), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Harness = "claude"
	legacy, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	resumed, err := LoadGenerationWithEnv(g.Directory, nil)
	if err != nil || resumed.ID != g.ID {
		t.Fatalf("old Claude generation cannot resume: %+v: %v", resumed, err)
	}
	if string(fileBytes(t, path)) != string(legacy) {
		t.Fatal("resume changed the pinned legacy manifest")
	}
}
