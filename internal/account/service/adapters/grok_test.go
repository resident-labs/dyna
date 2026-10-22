package adapters

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The output and cache shapes are copied from Grok 1.0.62 signed in with grok.com.
func TestGrokModelsReadListAndCache(t *testing.T) {
	home := t.TempDir()
	cache := `{"models":{
	  "grok-4.7":{"info":{"name":"Grok 4.7","description":"Latest frontier model","context_window":256000,
	    "reasoning_efforts":[{"value":"xhigh","label":"Extra High"},{"value":"high","label":"High","default":true}]}},
	  "grok-4.5":{"info":{"name":"Old","hidden":true,"reasoning_efforts":[{"value":"low"}]}}}}`
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(cache), 0600); err != nil {
		t.Fatal(err)
	}
	command := claudeFixtureCommand(t, "test \"$*\" = '--no-leader models'\nprintf '%s\\n' 'You are logged in with grok.com.' '' 'Default model: grok-4.7' '' 'Available models:' '  * grok-4.7 (default)' '  - grok-4.7-build-fast' '  - grok-4.5' '  - grok-4.5'\n")
	models, err := modelsGrok(context.Background(), command, []string{"GROK_HOME=" + home})
	if err != nil || len(models) != 3 {
		t.Fatalf("models = %+v, %v", models, err)
	}
	first := models[0]
	want := []Effort{{ID: "xhigh", Label: "Extra High", Args: []string{"--reasoning-effort", "xhigh"}}, {ID: "high", Label: "High", Default: true, Args: []string{"--reasoning-effort", "high"}}}
	if first.ID != "grok-4.7" || first.Name != "Grok 4.7" || first.Context != 256000 || first.Description == "" || !reflect.DeepEqual(first.Efforts, want) {
		t.Fatalf("first = %+v", first)
	}
	if models[1].ID != "grok-4.7-build-fast" || models[1].Name != models[1].ID || models[1].Efforts != nil {
		t.Fatalf("uncached model = %+v", models[1])
	}
	if models[2].Name != "grok-4.5" || models[2].Efforts != nil {
		t.Fatalf("hidden cache entry was used: %+v", models[2])
	}
	empty := claudeFixtureCommand(t, "printf '%s\\n' 'Default model: grok-4.7'\n")
	if models, err := modelsGrok(context.Background(), empty, []string{"GROK_HOME=" + home}); err == nil || len(models) != 0 {
		t.Fatalf("invented models: %+v", models)
	}
}
