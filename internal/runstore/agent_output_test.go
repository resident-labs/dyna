package runstore

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"dyna-agent/internal/workeroutput"
)

func TestAgentOutputPathCreateAndIncrementalRead(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_output-test")
	run, err := Create("output test", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)

	path, err := run.StartAgentOutput(3)
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

func TestOutputSnapshotsAndNormalizedIncrementalRead(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_normalized-output-test")
	run, err := Create("normalized output", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)

	decl := &workeroutput.Declaration{Dialect: workeroutput.DialectPiJSON}
	selected := workeroutput.ProfileSnapshot{Name: "sol", Model: "gpt-test", Output: decl}
	if err := run.SnapshotOutputProfiles([]workeroutput.ProfileSnapshot{selected}); err != nil {
		t.Fatal(err)
	}
	eventsPath, err := run.StartAgentNormalizedOutput(2, selected)
	if err != nil {
		t.Fatal(err)
	}

	configPath, err := AgentOutputConfigPath(run.Meta.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot workeroutput.SelectedSnapshot
	if err := json.Unmarshal(b, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 || snapshot.Profile.Name != "sol" || snapshot.Profile.Output == nil || snapshot.Profile.Output.Dialect != workeroutput.DialectPiJSON {
		t.Fatalf("selected snapshot = %#v", snapshot)
	}

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
	path, err := run.StartAgentOutput(4)
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
	selected := workeroutput.ProfileSnapshot{Name: "sol", Output: &workeroutput.Declaration{Dialect: workeroutput.DialectText}}
	path, err := run.StartAgentNormalizedOutput(5, selected)
	if err != nil {
		t.Fatal(err)
	}
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

func TestSnapshotOutputProfilesIsCreateOnce(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_output-snapshot-once")
	run, err := Create("snapshot once", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)

	first := workeroutput.ProfileSnapshot{Name: "sol", Output: &workeroutput.Declaration{Dialect: workeroutput.DialectPiJSON}}
	if err := run.SnapshotOutputProfiles([]workeroutput.ProfileSnapshot{first}); err != nil {
		t.Fatal(err)
	}
	second := workeroutput.ProfileSnapshot{Name: "sol", Output: &workeroutput.Declaration{Dialect: workeroutput.DialectText}}
	if err := run.SnapshotOutputProfiles([]workeroutput.ProfileSnapshot{second}); err != nil {
		t.Fatal(err)
	}
	path, err := OutputProfilesPath(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot workeroutput.RunSnapshot
	if err := json.Unmarshal(b, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Profiles) != 1 || snapshot.Profiles[0].Output == nil || snapshot.Profiles[0].Output.Dialect != workeroutput.DialectPiJSON {
		t.Fatalf("create-once snapshot was overwritten: %#v", snapshot)
	}
}
