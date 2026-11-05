//go:build linux

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

func TestWorkersOpenWithTheirProfileTheirOwnFolderAndTheirOwnSessionFile(t *testing.T) {
	model := pigtest.NewModel()
	agentDir := pigtest.AgentDir(t, model, "")
	box := pig.Config{AgentDir: agentDir, SessionDir: filepath.Join(agentDir, "sessions"), Instructions: "You are a worker."}
	profile := contract.WorkflowProfile{Name: "fast", ModelInstructions: "Use spaced prose.", Model: contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model", Thinking: contract.ThinkingLow}, CompactTokens: 150000}
	folder, dir := t.TempDir(), t.TempDir()
	models, err := os.ReadFile(filepath.Join(agentDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "models.json"), models, 0600); err != nil {
		t.Fatal(err)
	}
	c := workerConfig(box, profile, folder, dir)
	if c.ModelInstructions != profile.ModelInstructions || c.Dir != dir || c.Model != profile.Model || c.CompactTokens != 150000 || c.ModelsFile != filepath.Join(folder, "models.json") || !c.New || c.AgentDir != agentDir {
		t.Fatalf("config %+v", c)
	}
	files := map[string]bool{}
	for range 2 {
		worker, err := pig.Open(context.Background(), workerConfig(box, profile, folder, t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		defer worker.Close()
		files[worker.SessionFile()] = true
	}
	if len(files) != 2 {
		t.Fatalf("two workers share session files %v", files)
	}
}
