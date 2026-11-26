package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy/fakeagent"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeagent.EnvVar) != "" {
		fakeagent.Run(os.Stdin, os.Stdout)
		return
	}
	os.Exit(m.Run())
}

func TestOpenAgyNeedsItsFiles(t *testing.T) {
	_, err := openAgy(t.Context(), contract.BoxHelloResponse{Harness: contract.HarnessAgy}, "/box/work", t.TempDir(), nil, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "without its files") {
		t.Fatal(err)
	}
}

// Both room instruction files reach agy's system prompt append exactly once, and nothing is written into the profile folder.
func TestAgySessionGetsTheRoomInstructionsOnce(t *testing.T) {
	dir := t.TempDir()
	global, room := filepath.Join(dir, "global.md"), filepath.Join(dir, "room.md")
	if err := os.WriteFile(global, []byte("GLOBAL RULE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(room, []byte("ROOM RULE"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "agent.log")
	t.Setenv(fakeagent.EnvVar, "1")
	t.Setenv("FAKE_AGY_LOG", log)
	profile := t.TempDir()
	hello := contract.BoxHelloResponse{Harness: contract.HarnessAgy, Instructions: "be Dyna", Fresh: true, Agy: &contract.BoxAgy{Binary: exe, GeminiDir: profile}, Model: contract.ModelChoice{ID: "gemini-3.8-flash", Thinking: contract.ThinkingLow}}
	h, err := openAgy(t.Context(), hello, t.TempDir(), t.TempDir(), []string{global, room, filepath.Join(dir, "missing.md")}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	turn := []contract.TurnInput{{Text: "say hi"}}
	for _, id := range []contract.TurnID{"t1", "t2"} {
		if _, err := h.Start(t.Context(), id, turn, func() {}); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(log)
	var prompts []string
	for _, line := range bytes.Split(data, []byte("\n")) {
		var m struct {
			Method string `json:"method"`
			Params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &m) == nil && m.Method == "session/prompt" {
			var text string
			for _, b := range m.Params.Prompt {
				text += b.Text
			}
			prompts = append(prompts, text)
		}
	}
	if len(prompts) != 2 || strings.Contains(prompts[1], "RULE") {
		t.Fatalf("the rules ride on the first prompt only: %q", prompts)
	}
	appended := prompts[0]
	for _, want := range []string{"be Dyna", "GLOBAL RULE", "ROOM RULE", `<project_instructions path="` + global + `">`} {
		if strings.Count(appended, want) != 1 {
			t.Fatalf("%q appears %d times in %q", want, strings.Count(appended, want), appended)
		}
	}
	if _, err := os.Stat(filepath.Join(profile, "AGENTS.md")); err == nil {
		t.Fatal("an instruction file was written into the profile folder")
	}
}
