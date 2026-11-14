package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
)

// openAgent opens an in-box agent's PiG session in dir, in the box's working tree, with the tools of its turn and nothing else:
// no codemode, tool search, MCP server or package. Its models.json is its own, in agentDir/agents/<name>.
func openAgent(ctx context.Context, agentDir, workdir, core string, models ModelsConfig, notice func(string), turn contract.AgentTurn, dir string, toolCall func()) (agentSession, error) {
	folder := filepath.Join(agentDir, "agents", turn.Name)
	if err := os.MkdirAll(folder, 0700); err != nil {
		return nil, err
	}
	data, err := json.Marshal(models)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(folder, "models.json"), data, 0600); err != nil {
		return nil, err
	}
	c := workerConfig(pig.Config{AgentDir: agentDir, SessionDir: dir, Notice: notice}, turn.Profile, folder, workdir)
	c.New, c.Instructions, c.Tools, c.Bare = !turn.Persist, core+"\n\n"+turn.Instructions, turn.Tools, true
	session, err := pig.Open(ctx, c)
	if err != nil {
		return nil, err
	}
	session.Activity = func(_ contract.TurnID, kind contract.ActivityKind, _ string) {
		if kind == contract.ActivityTool {
			toolCall()
		}
	}
	return session, nil
}

// workerConfig is what a crew worker opens with, on the box-wide config: its own tree, its profile's model and compaction trigger, its own models.json
// in folder, and a session file of its own, so that workers sharing the session folder never resume one another's.
func workerConfig(box pig.Config, profile contract.WorkflowProfile, folder, dir string) pig.Config {
	box.Dir, box.Model, box.CompactTokens, box.New = dir, profile.Model, profile.CompactTokens, true
	box.ModelInstructions = profile.ModelInstructions
	box.ModelsFile = filepath.Join(folder, "models.json")
	return box
}
