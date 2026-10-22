package workflows

import (
	"bytes"
	"dyna-agent/internal/enginetest"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/runstore"
)

func execRuns(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := NewRunsCommand()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

// agedRun is a finished run whose meta claims it ended `age` ago. Its runner
// finishes it the way an earlier release does, by writing only meta.json, so
// the store imports the end it claims.
func agedRun(t *testing.T, id, status string, age time.Duration) *runstore.Run {
	t.Helper()
	t.Setenv("DYNA_RUN_ID", id)
	run, err := runstore.Create(id, "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Meta.Status = status
	run.Meta.EndedAt = time.Now().Add(-age)
	run.Meta.StartedAt = run.Meta.EndedAt.Add(-time.Minute)
	b, _ := json.Marshal(run.Meta)
	if err := os.WriteFile(filepath.Join(run.Dir, "meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return run
}

func listIDs(t *testing.T, args ...string) map[string]bool {
	t.Helper()
	out, _, err := execRuns(t, append([]string{"list", "--json"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("decode list: %v\n%s", err, out)
	}
	got := map[string]bool{}
	for _, r := range rows {
		archived, _ := r["archived"].(bool)
		got[r["id"].(string)] = archived
	}
	return got
}

func TestRunsArchiveDryRunThenArchiveAndList(t *testing.T) {
	serveRunEngine(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.ArchiveDirEnv, "")
	old := agedRun(t, "wf_cli-old", "ok", 72*time.Hour)
	oldCanceled := agedRun(t, "wf_cli-old-canceled", "canceled", 48*time.Hour)
	recent := agedRun(t, "wf_cli-recent", "ok", time.Hour)

	out, _, err := execRuns(t, "archive", "--older-than", "24h", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "would archive wf_cli-old ") || !strings.Contains(out, "would archive wf_cli-old-canceled") ||
		strings.Contains(out, "wf_cli-recent") || !strings.Contains(out, "would archive 2 run(s)") {
		t.Fatalf("dry-run output = %q", out)
	}
	for _, r := range []*runstore.Run{old, oldCanceled, recent} {
		if info, err := os.Lstat(r.Dir); err != nil || !info.IsDir() {
			t.Fatalf("dry run moved %s: %v", r.Meta.ID, err)
		}
	}

	if _, _, err := execRuns(t, "archive"); err == nil {
		t.Fatal("archive without a selector should fail")
	}

	out, _, err = execRuns(t, "archive", "--older-than", "1d")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "archived 2 run(s)") {
		t.Fatalf("archive output = %q", out)
	}

	if got := listIDs(t); len(got) != 1 {
		t.Fatalf("default list shows archived runs: %v", got)
	} else if _, ok := got[recent.Meta.ID]; !ok {
		t.Fatalf("default list lost the live run: %v", got)
	}
	if got := listIDs(t, "--archived"); len(got) != 2 || !got[old.Meta.ID] || !got[oldCanceled.Meta.ID] {
		t.Fatalf("--archived list = %v", got)
	}
	got := listIDs(t, "--all")
	if len(got) != 3 || !got[old.Meta.ID] || got[recent.Meta.ID] {
		t.Fatalf("--all list = %v", got)
	}
	text, _, err := execRuns(t, "list", "--all")
	if err != nil || !strings.Contains(text, "[archived]") {
		t.Fatalf("--all text = %q, %v", text, err)
	}

	// Every id-based read path still resolves the archived run.
	if out, _, err := execRuns(t, "show", old.Meta.ID, "--json"); err != nil || !strings.Contains(out, `"events"`) {
		t.Fatalf("show archived = %v\n%s", err, out)
	}
	if out, _, err := execRuns(t, "graph", old.Meta.ID); err != nil || out == "" {
		t.Fatalf("graph archived = %v", err)
	}
	// `runs wait` is deleted; observing a run is show/list over the store.
	if _, _, err := execRuns(t, "wait", old.Meta.ID); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("wait archived: %v, want unknown command", err)
	}

	out, _, err = execRuns(t, "unarchive", old.Meta.ID)
	if err != nil || !strings.Contains(out, "unarchived "+old.Meta.ID) {
		t.Fatalf("unarchive = %q, %v", out, err)
	}
	if got := listIDs(t); len(got) != 2 {
		t.Fatalf("list after unarchive = %v", got)
	} else if _, ok := got[old.Meta.ID]; !ok {
		t.Fatalf("unarchived run missing from list: %v", got)
	}
	if _, _, err := execRuns(t, "unarchive", old.Meta.ID); err == nil {
		t.Fatal("unarchiving a live run should fail")
	}
}

func TestRunsArchiveExplicitIDsSkipsRunning(t *testing.T) {
	serveRunEngine(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.ArchiveDirEnv, "")
	done := finishedRun(t, "wf_cli-done")
	t.Setenv("DYNA_RUN_ID", "wf_cli-running")
	running, err := runstore.Create("running", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { running.Finish("ok", "", nil) })

	out, errOut, err := execRuns(t, "archive", done.Meta.ID, running.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "archived "+done.Meta.ID) || !strings.Contains(errOut, "kept "+running.Meta.ID+": running") {
		t.Fatalf("stdout = %q stderr = %q", out, errOut)
	}
	if info, err := os.Lstat(running.Dir); err != nil || !info.IsDir() {
		t.Fatalf("running run moved: %v", err)
	}
	if _, _, err := execRuns(t, "archive", "--status", "running"); err == nil {
		t.Fatal("--status running should be rejected")
	}
}

func TestRunResumeFromArchivedRun(t *testing.T) {
	inRepo(t)
	// Two runs, each with one mock agent turn. A mock turn's default hold is only
	// needed where a test has to catch a worker in flight; this one waits for
	// each run to finish, so no turn needs to hold.
	defer func(previous time.Duration) { harness.MockDefaultDelay = previous }(harness.MockDefaultDelay)
	harness.MockDefaultDelay = 0
	ancestryStore, ancestryCalls := enginetest.PlanAncestryCalls(t)
	serveRunEngineAt(t, ancestryStore.DataDir, ancestryCalls)
	t.Setenv(runstore.ArchiveDirEnv, "")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	mockProfile(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "wf.js")
	if err := os.WriteFile(script, []byte(`return await agent("hi", {label:"hi"})`), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) string {
		t.Helper()
		cmd := NewRunCommand()
		var stdout bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{script, "--quiet", "--json", "--dir", dir}, extra...))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var result struct{ RunID string }
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.RunID == "" {
			t.Fatalf("run result = %s, %v", stdout.String(), err)
		}
		return result.RunID
	}
	sourceID := run()
	if _, _, err := execRuns(t, "archive", sourceID); err != nil {
		t.Fatal(err)
	}
	if !runstore.IsArchived(sourceID) {
		t.Fatal("source should be archived")
	}
	childID := run("--resume", sourceID)
	events, err := runstore.ReadEvents(childID)
	if err != nil {
		t.Fatal(err)
	}
	cached := false
	for _, e := range events {
		if e.T == "agent_end" && e.Cached {
			cached = true
		}
	}
	if !cached {
		t.Fatalf("resume from archived run replayed nothing: %#v", events)
	}
	if got, err := runstore.ResumedFrom(childID); err != nil || got != sourceID {
		t.Fatalf("child plan resumedFrom = %q, %v; want %q", got, err, sourceID)
	}
}
