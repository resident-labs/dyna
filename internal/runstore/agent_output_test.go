package runstore

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestAgentOutputPathCreateAndIncrementalRead(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_output-test")
	run, err := Create("output test", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)

	path, err := legacyArtifact(t, run, 3, "stdout.log"), error(nil)
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := AgentOutputPath(run.Meta.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if path != wantPath {
		t.Fatalf("output path = %q, want %q", path, wantPath)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("working\npart"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	output, offset, err := ReadAgentOutputFrom(run.Meta.ID, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if output != "working\npart" {
		t.Fatalf("initial output = %q", output)
	}

	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("ial\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	output, _, err = ReadAgentOutputFrom(run.Meta.ID, 3, offset)
	if err != nil {
		t.Fatal(err)
	}
	if output != "ial\n" {
		t.Fatalf("incremental output = %q", output)
	}
}

func TestNormalizedOutputIncrementalRead(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_normalized-output-test")
	run, err := Create("normalized output", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)

	eventsPath := legacyArtifact(t, run, 2, "output.jsonl")

	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"v\":1,\"kind\":\"message\",\"role\":\"assistant\",\"text\":\"first\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	events, offset, err := ReadAgentNormalizedOutputFrom(run.Meta.ID, 2, 0)
	if err != nil || len(events) != 1 || events[0].Text != "first" {
		t.Fatalf("normalized read = %#v, offset %d, %v", events, offset, err)
	}
	if more, next, err := ReadAgentNormalizedOutputFrom(run.Meta.ID, 2, offset); err != nil || len(more) != 0 || next != offset {
		t.Fatalf("empty incremental read = %#v, offset %d, %v", more, next, err)
	}
}

func TestAgentOutputLimitedReadAndCopy(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_output-limited")
	run, err := Create("limited output", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)
	path, err := legacyArtifact(t, run, 4, "stdout.log"), error(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, next, err := ReadAgentOutputLimitedFrom(run.Meta.ID, 4, 0, 5)
	if err != nil || string(got) != "hello" || next != 5 {
		t.Fatalf("limited raw = %q offset %d, %v", got, next, err)
	}
	rest, next, err := ReadAgentOutputLimitedFrom(run.Meta.ID, 4, next, 0)
	if err != nil || string(rest) != " world" || next != 11 {
		t.Fatalf("remaining raw = %q offset %d, %v", rest, next, err)
	}

	var buf bytes.Buffer
	written, copiedNext, err := CopyAgentOutputTo(&buf, run.Meta.ID, 4, 6, 5)
	if err != nil || written != 5 || copiedNext != 11 || buf.String() != "world" {
		t.Fatalf("copy = %q written %d offset %d, %v", buf.String(), written, copiedNext, err)
	}
}

func TestNormalizedOutputLimitedReadLeavesPartialUncommitted(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_normalized-limited")
	run, err := Create("limited normalized", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)
	path := legacyArtifact(t, run, 5, "output.jsonl")
	first := "{\"v\":1,\"kind\":\"message\",\"text\":\"one\"}\n"
	second := "{\"v\":1,\"kind\":\"message\",\"text\":\"two\"}\n"
	partial := "{\"v\":1,\"kind\":\"message\",\"text\":\"thr"
	if err := os.WriteFile(path, []byte(first+second+partial), 0o644); err != nil {
		t.Fatal(err)
	}

	events, offset, err := ReadAgentNormalizedOutputLimitedFrom(run.Meta.ID, 5, 0, int64(len(first)+10))
	if err != nil || len(events) != 1 || events[0].Text != "one" || offset != int64(len(first)) {
		t.Fatalf("byte-limited normalized = %#v offset %d, %v", events, offset, err)
	}

	events, offset, err = ReadAgentNormalizedOutputFrom(run.Meta.ID, 5, 0)
	if err != nil || len(events) != 2 || events[1].Text != "two" || offset != int64(len(first+second)) {
		t.Fatalf("unbounded normalized skipped partial = %#v offset %d, %v", events, offset, err)
	}
}

func TestAgentOutputReadsFromTheAgentsSessionWhenTheRunKeepsNone(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const run = "wf_session-output"
	session := AgentSessionID(run, 2)
	for name, want := range map[string]string{"stdout.log": "raw", "output.jsonl": "{}\n"} {
		path := filepath.Join(SessionsDir(), session, "agents", "1", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, get := range map[string]func(string, int) (string, error){
		"stdout.log": AgentOutputPath, "output.jsonl": AgentNormalizedOutputPath,
	} {
		got, err := get(run, 2)
		if want := filepath.Join(SessionsDir(), session, "agents", "1", name); err != nil || got != want {
			t.Fatalf("%s path = %q, %v; want the session's %q", name, got, err, want)
		}
	}
	// A run that kept its own artifact (a run from before agents were sessions) still reads it.
	own := filepath.Join(RunDir(run), "agents", "2", "stdout.log")
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := AgentOutputPath(run, 2); err != nil || got != own {
		t.Fatalf("path with the run's own artifact = %q, %v; want %q", got, err, own)
	}
}

// legacyArtifact creates an empty artifact in the run's own directory, where
// runs from before agents were sessions kept it, and returns its path.
func legacyArtifact(t *testing.T, run *Run, agent int, name string) string {
	t.Helper()
	path := filepath.Join(run.Dir, "agents", strconv.Itoa(agent), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
