package workflows

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

func execRunsOutput(args ...string) (string, error) {
	var stdout bytes.Buffer
	cmd := NewRunsCommand()
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), err
}

func TestRunsShowKeepsEventResultJSONContract(t *testing.T) {
	cmd := NewRunsCommand()
	show, _, err := cmd.Find([]string{"show"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"raw", "offset", "bytes", "follow", "agent"} {
		if show.Flags().Lookup(name) != nil {
			t.Fatalf("runs show unexpectedly gained --%s; worker output belongs on runs output", name)
		}
	}
	if _, _, err := cmd.Find([]string{"output"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunsOutputNormalizedJSONAndHuman(t *testing.T) {
	run := createOutputFixture(t, "wf_cli-output-norm", 2)
	path, err := runstore.AgentNormalizedOutputPath(run.Meta.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	first := `{"v":1,"kind":"message","role":"assistant","text":"hello"}` + "\n"
	partial := `{"v":1,"kind":"message","text":"inc`
	if err := os.WriteFile(path, []byte(first+partial), 0o644); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)

	out, err := execRunsOutput("output", run.Meta.ID, "2", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out)
	}
	if report["mode"] != "output" || report["complete"] != false || report["partialTail"] != true {
		t.Fatalf("report = %#v", report)
	}
	events, _ := report["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("events = %#v", report["events"])
	}
	event, _ := events[0].(map[string]any)
	if event["v"] != float64(1) || event["text"] != "hello" {
		t.Fatalf("event = %#v", event)
	}

	human, err := execRunsOutput("output", run.Meta.ID, "2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human, "ASSISTANT") || !strings.Contains(human, "hello") || strings.Contains(human, "inc") {
		t.Fatalf("human output = %q", human)
	}
}

func TestRunsOutputRawExactBytesAndBounds(t *testing.T) {
	run := createOutputFixture(t, "wf_cli-output-raw", 3)
	path, err := runstore.AgentOutputPath(run.Meta.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("hello world")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)

	out, err := execRunsOutput("output", run.Meta.ID, "3", "--raw")
	if err != nil {
		t.Fatal(err)
	}
	if out != string(payload) {
		t.Fatalf("raw stdout = %q", out)
	}

	bounded, err := execRunsOutput("output", run.Meta.ID, "3", "--raw", "--offset", "6", "--bytes", "5")
	if err != nil {
		t.Fatal(err)
	}
	if bounded != "world" {
		t.Fatalf("bounded raw = %q", bounded)
	}

	rawJSON, err := execRunsOutput("output", run.Meta.ID, "3", "--raw", "--json", "--bytes", "5")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &report); err != nil {
		t.Fatal(err)
	}
	if report["text"] != "hello" || report["truncated"] != true || report["complete"] != false || report["mode"] != "raw" {
		t.Fatalf("raw json = %#v", report)
	}
}

func TestRunsOutputRejectsInvalidAndMissing(t *testing.T) {
	run := createOutputFixture(t, "wf_cli-output-missing", 1)
	run.Finish("ok", "null", nil)

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"output", run.Meta.ID, "not-a-number"}, "positive integer"},
		{[]string{"output", run.Meta.ID, "0"}, "positive integer"},
		{[]string{"output", "not-a-run", "1"}, "invalid run id"},
		{[]string{"output", "wf_missing-run-id", "1"}, "not found"},
		{[]string{"output", run.Meta.ID, "9"}, "output.jsonl not found"},
		{[]string{"output", run.Meta.ID, "9", "--raw"}, "stdout.log not found"},
	}
	for _, tc := range cases {
		if _, err := execRunsOutput(tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("args %v error = %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestRunsOutputSkipsMalformedJSONL(t *testing.T) {
	run := createOutputFixture(t, "wf_cli-output-malformed", 6)
	path, err := runstore.AgentNormalizedOutputPath(run.Meta.ID, 6)
	if err != nil {
		t.Fatal(err)
	}
	contents := "not json\n" + `{"v":1,"kind":"raw","raw":"kept"}` + "\n" + `{"v":1,"kind":"message"`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)

	out, err := execRunsOutput("output", run.Meta.ID, "6", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	events, _ := report["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("events = %#v", report["events"])
	}
	event, _ := events[0].(map[string]any)
	if event["kind"] != "raw" || event["raw"] != "kept" {
		t.Fatalf("event = %#v", event)
	}
	if report["partialTail"] != true || report["complete"] != false {
		t.Fatalf("malformed tail flags = %#v", report)
	}
}

func TestRunsOutputFollowJSONReturnsOnFinishedPartialTail(t *testing.T) {
	run := createOutputFixture(t, "wf_cli-output-follow-partial", 7)
	path, err := runstore.AgentNormalizedOutputPath(run.Meta.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	first := `{"v":1,"kind":"message","role":"assistant","text":"hello"}` + "\n"
	partial := `{"v":1,"kind":"message","text":"inc`
	if err := os.WriteFile(path, []byte(first+partial), 0o644); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)

	out, err := execRunsOutput("output", run.Meta.ID, "7", "--follow", "--json", "--timeout", "2")
	if err != nil {
		t.Fatalf("follow finished partial: %v\n%s", err, out)
	}
	report := lastOutputEnvelope(t, out)
	if report["complete"] != false || report["partialTail"] != true {
		t.Fatalf("follow partial report = %#v\n%s", report, out)
	}
	events, _ := report["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("follow partial events = %#v", report["events"])
	}
}

func TestRunsOutputOffsetPastEOFIsEmptyWindow(t *testing.T) {
	run := createOutputFixture(t, "wf_cli-output-offset-eof", 8)
	normPath, err := runstore.AgentNormalizedOutputPath(run.Meta.ID, 8)
	if err != nil {
		t.Fatal(err)
	}
	rawPath, err := runstore.AgentOutputPath(run.Meta.ID, 8)
	if err != nil {
		t.Fatal(err)
	}
	record := `{"v":1,"kind":"message","role":"assistant","text":"hello"}` + "\n"
	if err := os.WriteFile(normPath, []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rawPath, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)

	out, err := execRunsOutput("output", run.Meta.ID, "8", "--json", "--offset", "999")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report["offset"] != float64(999) || report["nextOffset"] != float64(len(record)) || report["fileSize"] != float64(len(record)) {
		t.Fatalf("normalized past-EOF window = %#v", report)
	}
	events, _ := report["events"].([]any)
	if len(events) != 0 {
		t.Fatalf("normalized past-EOF reprinted events = %#v", report["events"])
	}

	human, err := execRunsOutput("output", run.Meta.ID, "8", "--offset", "999")
	if err != nil {
		t.Fatal(err)
	}
	if human != "" {
		t.Fatalf("normalized past-EOF human = %q", human)
	}

	raw, err := execRunsOutput("output", run.Meta.ID, "8", "--raw", "--offset", "50")
	if err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		t.Fatalf("raw past-EOF reprinted %q", raw)
	}

	rawJSON, err := execRunsOutput("output", run.Meta.ID, "8", "--raw", "--json", "--offset", "50")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(rawJSON), &report); err != nil {
		t.Fatal(err)
	}
	if report["offset"] != float64(50) || report["nextOffset"] != float64(11) || report["text"] != "" {
		t.Fatalf("raw past-EOF window = %#v", report)
	}
}

func TestRunsOutputHumanCoalescesReasoningAndMatchesTUILabels(t *testing.T) {
	run := createOutputFixture(t, "wf_cli-output-human-coalesce", 9)
	path, err := runstore.AgentNormalizedOutputPath(run.Meta.ID, 9)
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Join([]string{
		`{"v":1,"kind":"reasoning","state":"delta","text":"I will run"}`,
		`{"v":1,"kind":"reasoning","state":"delta","text":" the command."}`,
		`{"v":1,"kind":"status","name":"reasoning","state":"completed"}`,
		`{"v":1,"kind":"message","role":"assistant","text":"Done."}`,
		`{"v":2,"kind":"message","role":"assistant","text":"future event"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)

	human, err := execRunsOutput("output", run.Meta.ID, "9")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(human, "THINKING") != 1 || !strings.Contains(human, "I will run the command.") {
		t.Fatalf("human did not coalesce reasoning: %q", human)
	}
	if strings.Contains(human, "STATUS  reasoning completed") {
		t.Fatalf("reasoning completion leaked: %q", human)
	}
	if !strings.Contains(human, "I will run the command.\n\nASSISTANT") {
		t.Fatalf("human blocks were not blank-line spaced: %q", human)
	}
	if !strings.Contains(human, "EVENT V2  message") {
		t.Fatalf("human label = %q", human)
	}

	jsonOut, err := execRunsOutput("output", run.Meta.ID, "9", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &report); err != nil {
		t.Fatal(err)
	}
	events, _ := report["events"].([]any)
	if len(events) != 5 {
		t.Fatalf("--json coalesced artifact events: %#v", report["events"])
	}
}

func TestHumanOutputWriterCoalescesAcrossFollowPolls(t *testing.T) {
	var buf bytes.Buffer
	h := humanOutputWriter{w: &buf}
	if err := h.write([]workeroutput.Event{
		{V: 1, Kind: "reasoning", State: "delta", Text: "I will"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.write([]workeroutput.Event{
		{V: 1, Kind: "reasoning", State: "delta", Text: " run"},
		{V: 1, Kind: "status", Name: "reasoning", State: "completed"},
		{V: 1, Kind: "message", Role: "assistant", Text: "Done."},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.flush(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Count(got, "THINKING") != 1 || !strings.Contains(got, "I will run") {
		t.Fatalf("follow polls did not coalesce: %q", got)
	}
	if !strings.Contains(got, "I will run\n\nASSISTANT") {
		t.Fatalf("follow polls lost blank-line spacing: %q", got)
	}
}

func lastOutputEnvelope(t *testing.T, out string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var report map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &report); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	return report
}

func createOutputFixture(t *testing.T, runID string, agentID int) *runstore.Run {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", runID)
	run, err := runstore.Create("cli output", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "null", nil) })
	dir := filepath.Join(run.Dir, "agents", strconv.Itoa(agentID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stdout.log", "output.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return run
}
