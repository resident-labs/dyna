package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

// The fixtures pin the stdin shapes Claude Code 2.1.299 accepts. The SDK
// writes one JSON object per line: request_id, type control_request, then
// request. interrupt stops the current turn. set_model names the next model.
// A later CLI can change the shape; these tests fail when the bytes drift.

func TestClaudeCancelSendsInterruptControl(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "stdin.txt")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	seenPrompt := make(chan struct{})
	var prompted bool
	spec := commandSpec{prompt: "work", streamInput: true, argv: []string{"/bin/sh", "-c", claudeInterruptScript}}
	p := profile.Profile{Harness: profile.HarnessCustom, Env: map[string]string{"CONTROL_CAPTURE": capture}}
	done := make(chan attempt, 1)
	go func() {
		done <- runOnce(ctx, p, dir, spec, newSessionTracker(nil), nil, nil, true, "", "",
			&workeroutput.Declaration{Dialect: workeroutput.DialectClaudeStreamJSON},
			func(line string) {
				if !prompted && strings.Contains(line, `"subtype":"got-prompt"`) {
					prompted = true
					close(seenPrompt)
				}
			}, newDeliveryGate(JournalOptions{}), &runState{}, ExecutionOptions{SkipComputer: true})
	}()
	select {
	case <-seenPrompt:
	case got := <-done:
		t.Fatalf("worker ended before the prompt was read: %+v", got)
	case <-time.After(30 * time.Second):
		t.Fatal("worker did not read the prompt")
	}
	cancel()
	var got attempt
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
	if !got.contextDone {
		t.Fatalf("cancel did not finish the attempt: %+v", got.runErr)
	}
	line := capturedControl(t, capture, "interrupt")
	assertControlFixture(t, line, "claude-control-interrupt.json")
	if !strings.Contains(got.stdout, `"subtype":"success"`) {
		t.Fatalf("worker did not wait for the interrupt response:\n%s", got.stdout)
	}
}

func TestClaudeStreamSendsSetModelBeforePrompt(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "stdin.txt")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	spec := commandSpec{prompt: "work", streamInput: true, argv: []string{"/bin/sh", "-c", claudeSetModelScript}}
	p := profile.Profile{
		Harness: profile.HarnessCustom,
		Model:   "fixture-model",
		Env:     map[string]string{"CONTROL_CAPTURE": capture},
	}
	done := make(chan attempt, 1)
	go func() {
		done <- runOnce(ctx, p, dir, spec, newSessionTracker(nil), nil, nil, true, "", "",
			&workeroutput.Declaration{Dialect: workeroutput.DialectClaudeStreamJSON}, nil,
			newDeliveryGate(JournalOptions{}), &runState{}, ExecutionOptions{SkipComputer: true})
	}()
	var got attempt
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("worker did not finish")
	}
	if got.runErr != nil {
		t.Fatalf("set_model turn failed: %v\n%s", got.runErr, got.stdout)
	}
	if !strings.Contains(got.stdout, `"result":"model-set"`) {
		t.Fatalf("set_model was not written before the prompt:\n%s\ncapture:\n%s", got.stdout, readCapture(t, capture))
	}
	line := capturedControl(t, capture, "set_model")
	assertControlFixture(t, line, "claude-control-set-model.json")
}

// claudeInterruptScript records stdin. After the user message it waits for
// an interrupt control request, answers it, and ends the turn.
const claudeInterruptScript = `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"control-session"}'
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$CONTROL_CAPTURE"
  case "$line" in
    *'"subtype":"interrupt"'*)
      id=$(printf '%s\n' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      printf '%s\n' "{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"$id\",\"response\":{}}}"
      printf '%s\n' '{"type":"result","subtype":"error_during_execution","session_id":"control-session","result":"interrupted","is_error":true}'
      exit 0
      ;;
    *'"type":"user"'*)
      printf '%s\n' "$line" | sed 's/"type":"user"/"type":"user","isReplay":true/'
      printf '%s\n' '{"type":"system","subtype":"got-prompt","session_id":"control-session"}'
      ;;
  esac
done
`

// claudeSetModelScript records stdin and says whether set_model arrived
// before the user message. It exits on that user message either way.
const claudeSetModelScript = `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"control-session"}'
got_model=0
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$CONTROL_CAPTURE"
  case "$line" in
    *'"subtype":"set_model"'*)
      got_model=1
      id=$(printf '%s\n' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      printf '%s\n' "{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"$id\",\"response\":{\"model\":\"fixture-model\"}}}"
      ;;
    *'"type":"user"'*)
      printf '%s\n' "$line" | sed 's/"type":"user"/"type":"user","isReplay":true/'
      if [ "$got_model" -eq 1 ]; then
        printf '%s\n' '{"type":"result","subtype":"success","session_id":"control-session","result":"model-set","is_error":false}'
      else
        printf '%s\n' '{"type":"result","subtype":"success","session_id":"control-session","result":"saw-user-first","is_error":false}'
      fi
      exit 0
      ;;
  esac
done
`

func TestClaudeControlLinesMatchFixtures(t *testing.T) {
	interrupt := workeroutput.ClaudeControlInterrupt("dyna-1")
	setModel := workeroutput.ClaudeControlSetModel("dyna-1", "fixture-model")
	assertExactFixture(t, interrupt, "claude-control-interrupt.json")
	assertExactFixture(t, setModel, "claude-control-set-model.json")
	id, success, ok := workeroutput.ClaudeControlResponse(`{"type":"control_response","response":{"subtype":"success","request_id":"dyna-1","response":{}}}`)
	if !ok || !success || id != "dyna-1" {
		t.Fatalf("success response = %q %v %v", id, success, ok)
	}
	id, success, ok = workeroutput.ClaudeControlResponse(`{"type":"control_response","response":{"subtype":"error","request_id":"dyna-2","error":"no"}}`)
	if !ok || success || id != "dyna-2" {
		t.Fatalf("error response = %q %v %v", id, success, ok)
	}
	if _, _, ok = workeroutput.ClaudeControlResponse(`{"type":"result","result":"x"}`); ok {
		t.Fatal("a result record was read as a control response")
	}
}

func assertExactFixture(t *testing.T, line []byte, name string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(line)) != strings.TrimSpace(string(want)) {
		t.Fatalf("line = %s; fixture %s", bytes.TrimSpace(line), bytes.TrimSpace(want))
	}
}

func capturedControl(t *testing.T, path, subtype string) []byte {
	t.Helper()
	text := readCapture(t, path)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var probe struct {
			Type    string `json:"type"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if json.Unmarshal([]byte(line), &probe) != nil {
			continue
		}
		if probe.Type == "control_request" && probe.Request.Subtype == subtype {
			return []byte(line)
		}
	}
	t.Fatalf("stdin capture has no %s control_request:\n%s", subtype, text)
	return nil
}

func assertControlFixture(t *testing.T, line []byte, name string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	gotType, gotReq := controlRequestBody(t, line)
	wantType, wantReq := controlRequestBody(t, want)
	if gotType != wantType || !bytes.Equal(gotReq, wantReq) {
		t.Fatalf("control request = type %s request %s; fixture type %s request %s", gotType, gotReq, wantType, wantReq)
	}
}

func controlRequestBody(t *testing.T, line []byte) (string, []byte) {
	t.Helper()
	var env struct {
		Type    string          `json:"type"`
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatalf("control line: %v\n%s", err, line)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, env.Request); err != nil {
		t.Fatal(err)
	}
	return env.Type, compact.Bytes()
}

func readCapture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}
