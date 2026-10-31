package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestProfileSurfaceValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	config := `{"p":{"provider":"anthropic","model":"a","allowedModels":["a"],"thinking":"low","agentSurface":"caller"}}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil || profiles.Surface("p") != contract.AgentSurfaceCaller {
		t.Fatalf("surface: %v %v", profiles, err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(config, "caller", "unknown", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(path); err == nil {
		t.Fatal("unknown surface accepted")
	}
}

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

func TestProfileRatings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	config := `{"writer":{"provider":"anthropic","model":"m","allowedModels":["m"],"thinking":"low","description":"Clear writing","taste":10,"diagnostics":8,"intelligence":7,"cost":6}}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	got := profiles.List()[0]
	if got.Description != "Clear writing" || got.Taste != 10 || got.Diagnostics != 8 || got.Intelligence != 7 || got.Cost != 6 {
		t.Fatalf("ratings lost: %+v", got)
	}
	for _, invalid := range []string{"-1", "11"} {
		bad := strings.Replace(config, `"taste":10`, `"taste":`+invalid, 1)
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProfiles(path); err == nil {
			t.Fatalf("accepted rating %s", invalid)
		}
	}
}
