package agent_test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/contract"
)

func TestTextOnlyTransferRealPi(t *testing.T) {
	cli := os.Getenv("DYNA_PI_E2E_CLI")
	if cli == "" {
		t.Skip("DYNA_PI_E2E_CLI names the built Pi cli.js")
	}
	dir := t.TempDir()
	config, err := json.Marshal(agent.Models(contract.TextOnlyModel{Provider: contract.ModelProviderOpenAI, Model: "glm-5.3"}))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "models.json")
	if err := os.WriteFile(file, config, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", cli, "--mode", "rpc", "--models-file", file, "--provider", "openai", "--model", "gpt-5.5", "--no-session", "--no-extensions", "--no-mcp", "--no-skills", "--no-prompt-templates", "--no-context-files")
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+filepath.Join(dir, "agent"), "PI_OFFLINE=1", "PI_NO_ENV_CREDENTIALS=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	send := func(s string) {
		t.Helper()
		if _, err := stdin.Write([]byte(s + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	response := func(id string) json.RawMessage {
		t.Helper()
		for scanner.Scan() {
			var msg struct {
				ID      string          `json:"id"`
				Success bool            `json:"success"`
				Data    json.RawMessage `json:"data"`
				Error   string          `json:"error"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				t.Fatal(err)
			}
			if msg.ID == id {
				if !msg.Success {
					t.Fatal(msg.Error)
				}
				return msg.Data
			}
		}
		t.Fatalf("Pi stopped: %v", scanner.Err())
		return nil
	}
	send(`{"id":"models","type":"get_available_models"}`)
	var available struct {
		Models []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"models"`
	}
	if err := json.Unmarshal(response("models"), &available); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range available.Models {
		if m.Provider == "openai" && m.ID == "gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Fatal("built-in model missing")
	}
	send(`{"id":"before","type":"get_state"}`)
	var before struct {
		Model map[string]any `json:"model"`
	}
	if err := json.Unmarshal(response("before"), &before); err != nil {
		t.Fatal(err)
	}
	send(`{"id":"move","type":"transfer_model","provider":"openai","modelId":"glm-5.3"}`)
	response("move")
	send(`{"id":"state","type":"get_state"}`)
	var state struct {
		Model struct {
			ID    string   `json:"id"`
			Input []string `json:"input"`
		} `json:"model"`
	}
	data := response("state")
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Model.ID != "glm-5.3" || len(state.Model.Input) != 1 || state.Model.Input[0] != "text" {
		t.Fatalf("model: %+v", state.Model)
	}
	var after struct {
		Model map[string]any `json:"model"`
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	// Built-in models expose type=chat; Pi treats a models.json entry with no type as chat.
	for _, field := range []string{"id", "name", "input", "cost", "type"} {
		delete(before.Model, field)
		delete(after.Model, field)
	}
	if !reflect.DeepEqual(before.Model, after.Model) {
		t.Fatalf("fallback metadata differs:\nbefore: %+v\nafter: %+v", before.Model, after.Model)
	}
}
