package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// Pi discovers room instructions from its working directory.
func TestOpenUsesWorkDirectory(t *testing.T) {
	work := t.TempDir()
	repo := filepath.Join(work, "owner", "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	agent := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agent)
	for path, content := range map[string]string{
		filepath.Join(agent, "AGENTS.md"): "global",
		filepath.Join(work, "AGENTS.md"):  "room",
		filepath.Join(repo, "AGENTS.md"):  "repository",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `read -r request; files="$(cat "$PI_CODING_AGENT_DIR/AGENTS.md")-$(cat ../../AGENTS.md)-$(cat AGENTS.md)"; printf '{"id":"1","type":"response","success":true,"data":{"sessionFile":"%s"}}\n' "$files"; read -r wait`
	h, err := Open(context.Background(), "sh", []string{"-c", script}, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if got := h.SessionFile(); got != "global-room-repository" {
		t.Fatalf("work directory exposed %q, want global, room, and repository instructions", got)
	}
}

// A notify's message is a string where every event has an object, and a failed decode would end the reader, and the box.
func TestAWarningOutsideATurnReachesTheNotice(t *testing.T) {
	script := `read -r request
printf '%s\n' '{"type":"extension_ui_request","id":"a","method":"notify","message":"MCP servers need attention:\n  broken: failed: spawn ENOENT\nRun /mcp to fix.","notifyType":"warning"}'
printf '%s\n' '{"type":"extension_ui_request","id":"b","method":"notify","message":"just so you know","notifyType":"info"}'
printf '%s\n' '{"type":"extension_ui_request","id":"c","method":"setStatus","statusKey":"k"}'
printf '{"id":"1","type":"response","success":true,"data":{"sessionFile":"file"}}\n'
read -r wait`
	notices := make(chan string, 4)
	h, err := Open(context.Background(), "sh", []string{"-c", script}, "", t.TempDir(), func(text string) { notices <- text })
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if got := <-notices; got != "MCP servers need attention:\n  broken: failed: spawn ENOENT" {
		t.Fatalf("%q", got)
	}
	select {
	case got := <-notices:
		t.Fatalf("an info line came through: %q", got)
	default:
	}
}

func TestSetPromptSectionsRPC(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	h := &Harness{stdin: inputWriter, pending: make(map[string]chan event), events: make(chan event, 1), done: make(chan struct{}), stop: make(chan struct{})}
	go h.read(outputReader)
	defer inputWriter.Close()
	defer outputWriter.Close()
	reader := bufio.NewReader(inputReader)
	for _, tc := range []struct {
		sections []contract.ContextSection
		want     string
		success  bool
	}{
		{[]contract.ContextSection{{Key: "goal", Text: "work"}}, `[{"key":"goal","text":"work"}]`, true},
		{nil, `[]`, false},
	} {
		done := make(chan error, 1)
		go func() { done <- h.SetPromptSections(context.Background(), tc.sections) }()
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			ID       string          `json:"id"`
			Type     string          `json:"type"`
			Sections json.RawMessage `json:"sections"`
		}
		if err := json.Unmarshal(line, &c); err != nil {
			t.Fatal(err)
		}
		if c.Type != "set_prompt_sections" || string(c.Sections) != tc.want {
			t.Fatalf("command = %s", line)
		}
		if err := json.NewEncoder(outputWriter).Encode(event{ID: c.ID, Type: "response", Success: tc.success, Error: "rejected"}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; (err == nil) != tc.success {
			t.Fatalf("success=%v error=%v", tc.success, err)
		}
	}
}

// The fixture contains selected, sanitized events from Pi 1.4.0-dyna.10,
// session ses_20261030-143118-fad2075a, turn 7.
func TestRecordedTurnAndSteer(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	h := &Harness{
		stdin: inputWriter, pending: make(map[string]chan event),
		events: make(chan event, 256), done: make(chan struct{}), stop: make(chan struct{}),
	}
	go h.read(outputReader)
	defer inputWriter.Close()
	defer outputWriter.Close()
	reader := bufio.NewReader(inputReader)
	result := make(chan contract.TurnEnded, 1)
	errs := make(chan error, 1)
	go func() {
		r, err := h.Start(context.Background(), "turn_test", []contract.TurnInput{{Delivery: 21, Text: "start"}}, func() {})
		if err != nil {
			errs <- err
			return
		}
		result <- r
	}()
	readCommand := func() command {
		t.Helper()
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var c command
		if err := json.Unmarshal(line, &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	respond := func(c command) {
		t.Helper()
		if err := json.NewEncoder(outputWriter).Encode(event{ID: c.ID, Type: "response", Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	c := readCommand()
	if c.Type != "prompt" || len(c.Inputs) != 1 || c.Inputs[0].DeliveryID != "21" {
		t.Fatalf("prompt: %+v", c)
	}
	respond(c)
	steered := make(chan error, 1)
	go func() { steered <- h.Steer(context.Background(), contract.TurnInput{Delivery: 22, Text: "steer"}) }()
	c = readCommand()
	if c.Type != "steer" || c.DeliveryID != "22" || c.Message != "steer" {
		t.Fatalf("steer: %+v", c)
	}
	respond(c)
	if err := <-steered; err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/p4-turn7.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outputWriter.Write(fixture); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		t.Fatal(err)
	case got := <-result:
		if got.Outcome != contract.TurnOutcome("completed") || !strings.HasPrefix(got.Final, "PR [#1289]") {
			t.Fatalf("unexpected result: %+v", got)
		}
		if got.Usage.Model != "gpt-6.1-terra" || got.Usage.InputTokens != 17936 || got.Usage.OutputTokens != 212 || got.Usage.CacheReadTokens != 26496 {
			t.Fatalf("unexpected usage: %+v", got.Usage)
		}
	}
}

// replayTurn runs one turn against a scripted RPC stream and returns the settled result.
func replayTurn(t *testing.T, stream string) contract.TurnEnded {
	t.Helper()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	h := &Harness{
		stdin: inputWriter, pending: make(map[string]chan event),
		events: make(chan event, 256), done: make(chan struct{}), stop: make(chan struct{}),
	}
	go h.read(outputReader)
	defer inputWriter.Close()
	defer outputWriter.Close()
	result := make(chan contract.TurnEnded, 1)
	errs := make(chan error, 1)
	go func() {
		r, err := h.Start(context.Background(), "turn_error", []contract.TurnInput{{Delivery: 31, Text: "start"}}, func() {})
		if err != nil {
			errs <- err
			return
		}
		result <- r
	}()
	line, err := bufio.NewReader(inputReader).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var c command
	if err := json.Unmarshal(line, &c); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(outputWriter).Encode(event{ID: c.ID, Type: "response", Success: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := outputWriter.Write([]byte(stream)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		t.Fatal(err)
	case got := <-result:
		return got
	}
	return contract.TurnEnded{}
}

func TestErrorStopReasonCarriesModelFailure(t *testing.T) {
	got := replayTurn(t, `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"provider refused"}}`+"\n"+
		`{"type":"agent_settled","aborted":false}`+"\n")
	if got.Outcome != contract.TurnOutcome("failed") || got.Cause != contract.CauseModelFailed || got.Error != "provider refused" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestAbortAfterErrorClearsFailure(t *testing.T) {
	got := replayTurn(t, `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"provider refused"}}`+"\n"+
		`{"type":"agent_settled","aborted":true}`+"\n")
	if got.Outcome != contract.TurnOutcome("stopped") || got.Cause != "" || got.Error != "" {
		t.Fatalf("stopped turn kept its failure: %+v", got)
	}
}
