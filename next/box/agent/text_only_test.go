package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
)

func TestTextOnlyProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"vision":{"provider":"openai","model":"vision","allowedModels":["vision"],"thinking":"high"},"text":{"provider":"openai","model":"glm-5.3","allowedModels":["glm-5.3","alternate","alternate"],"thinking":"high","images":false},"text2":{"provider":"anthropic","model":"other","allowedModels":["other"],"thinking":"off","images":false}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := engine.LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(profiles.List())
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatal(err)
	}
	if entries[0]["images"] != false {
		t.Fatalf("text-only flag lost: %s", b)
	}
	// All profiles go to every box at hello: moving from vision to text therefore needs no rewrite.
	models := agent.Models(profiles.TextOnlyModels()...)
	openai := models.Providers["openai"]
	if len(openai.Models) != 2 || openai.Models[1].ID != "glm-5.3" || len(openai.Models[1].Input) != 1 || openai.Models[1].Input[0] != "text" || !openai.Models[1].Reasoning {
		t.Fatalf("models: %+v", openai.Models)
	}
	anthropic := models.Providers["anthropic"]
	if len(anthropic.Models) != 1 || anthropic.Models[0].ID != "other" || len(anthropic.Models[0].Input) != 1 || anthropic.Models[0].Input[0] != "text" || !anthropic.Models[0].Reasoning {
		t.Fatalf("anthropic: %+v", anthropic.Models)
	}
	worker := agent.WorkerModels("worker", profiles.TextOnlyModels()...)
	for _, provider := range []string{"anthropic", "openai"} {
		if worker.Providers[provider].Headers[contract.WorkerHeader] != "worker" || len(worker.Providers[provider].Models) != len(models.Providers[provider].Models) {
			t.Fatalf("worker %s: %+v", provider, worker.Providers[provider])
		}
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	var decoded agent.ModelsConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Providers["openai"].Models) != 2 {
		t.Fatalf("models.json: %s", encoded)
	}
}
