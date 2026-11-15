package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const core = "You are a Dyna agent. The shared base prompt.\n\n# How work goes\n\nThe core text."

const promptSchema = `{"type":"object","required":["count"],"properties":{"count":{"type":"number"}}}`

// A crew worker's prompt is the core every agent shares, then Claude Code's workflow subagent prompt for plain text, with one line for a schema and a note on its
// tree.
func TestACrewWorkersPromptIsTheWorkflowSubagentPrompt(t *testing.T) {
	for name, got := range map[string]string{
		"worker-plain.txt":       crewInstructions(core, nil, "", "/box/work/sweep"),
		"worker-schema.txt":      crewInstructions(core, json.RawMessage(promptSchema), "", "/box/work/sweep"),
		"worker-schema-tree.txt": crewInstructions(core, json.RawMessage(promptSchema), "/box/work/sweep/w3", "/box/work/sweep"),
	} {
		if os.Getenv("DYNA_UPDATE_GOLDEN") != "" {
			if err := os.WriteFile("testdata/"+name, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
		}
		if !strings.HasPrefix(got, core+"\n\n# Your role: workflow worker\n\nYou are a worker spawned by a workflow script.") || strings.Contains(got, "submit") {
			t.Errorf("%s is not the core and the worker prompt", name)
		}
	}
}
