package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestProfileMappingPreservesThinkingAndReportsUnsupported(t *testing.T) {
	dir := t.TempDir()
	data := `{"version":3,"profiles":[
{"name":"rewrite","model":"opus","extraArgs":["--effort","xhigh"]},
{"name":"terra","model":"openai-codex/gpt-6.1-terra","extraArgs":["--thinking=high"]},
{"name":"wonder","model":"meta/muse-wonder-full-1.0"},
{"name":"disabled","model":"unused","disabled":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, disabled, unknown, err := readProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].Model != "claude-opus-5-5" || profiles[0].Thinking != contract.ThinkingXHigh || profiles[1].Model != "gpt-6.1-terra" || profiles[1].Thinking != contract.ThinkingHigh || len(disabled) != 1 || len(unknown) != 1 {
		t.Fatalf("profiles=%+v disabled=%+v unknown=%+v", profiles, disabled, unknown)
	}
}
