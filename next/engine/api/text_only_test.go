package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/engine"
)

func TestTextOnlyProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"vision":{"provider":"openai","model":"vision","allowedModels":["vision"],"thinking":"high"},"text":{"provider":"openai","model":"glm-5.3","allowedModels":["glm-5.3"],"thinking":"high","images":false}}`
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
	models := agent.Models(profiles.List()...)
	openai := models.Providers["openai"]
	if len(openai.Models) != 1 || openai.Models[0].ID != "glm-5.3" || len(openai.Models[0].Input) != 1 || openai.Models[0].Input[0] != "text" {
		t.Fatalf("models: %+v", openai.Models)
	}
	worker := agent.WorkerModels("worker", profiles.List()...)
	if len(worker.Providers["openai"].Models) != 1 {
		t.Fatal("worker lost text-only model")
	}
}
