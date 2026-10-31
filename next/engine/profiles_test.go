package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestProfilesRejectModelOutsideAllowList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(`{"p":{"provider":"anthropic","model":"a","allowedModels":["b"],"thinking":"low"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(path); err == nil {
		t.Fatal("expected invalid profile")
	}
}

func TestProfilesReturnModelChoice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(`{"p":{"provider":"anthropic","model":"a","allowedModels":["a"],"thinking":"xhigh"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	choice, allowed, err := p.Model("p")
	if err != nil || choice.ID != "a" || choice.Thinking != contract.ThinkingXHigh || len(allowed) != 1 || allowed[0] != "a" {
		t.Fatalf("%+v %v %v", choice, allowed, err)
	}
}

func TestProfilesRejectNegativeLimits(t *testing.T) {
	for _, field := range []string{"maxConcurrent", "maxCallsPerRun"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.json")
			data := `{"p":{"provider":"anthropic","model":"a","allowedModels":["a"],"thinking":"low","` + field + `":-1}}`
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfiles(path); err == nil {
				t.Fatal("negative limit accepted")
			}
		})
	}
}
