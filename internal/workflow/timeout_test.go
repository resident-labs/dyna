package workflow

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/harness/fakeworker"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

func TestExecuteCapturesHarnessStdout(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-output")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	script := `return await agent("show work", {profile: "worker"});`
	run, err := runstore.Create("output capture", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "", nil)
	worker := fakeworker.InstallClaude(t, `printf 'first\nsecond'`)
	worker.Name, worker.Default = "worker", true
	store := &profile.Store{Profiles: []profile.Profile{worker}}
	result, err := Execute(context.Background(), Options{ScriptSrc: script, Store: store, Run: run, WorkDir: t.TempDir()})
	if err != nil || result != `"first\nsecond"` {
		t.Fatalf("Execute() = %s, %v", result, err)
	}
	output, _, err := runstore.ReadAgentOutputFrom(run.Meta.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `first\nsecond`) {
		t.Fatalf("captured stdout = %q", output)
	}
	events, _, err := runstore.ReadAgentNormalizedOutputFrom(run.Meta.ID, 1, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("normalized output = %#v, %v", events, err)
	}
	configPath, err := runstore.AgentOutputConfigPath(run.Meta.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var selected workeroutput.SelectedSnapshot
	if err := json.Unmarshal(b, &selected); err != nil || selected.Profile.Output == nil || selected.Profile.Output.Dialect != workeroutput.DialectClaudeStreamJSON {
		t.Fatalf("selected output snapshot = %#v, %v", selected, err)
	}
	runSnapshotPath, err := runstore.OutputProfilesPath(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(runSnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var runSnapshot workeroutput.RunSnapshot
	if err := json.Unmarshal(b, &runSnapshot); err != nil || len(runSnapshot.Profiles) != 1 || runSnapshot.Profiles[0].Output == nil || runSnapshot.Profiles[0].Output.Dialect != workeroutput.DialectClaudeStreamJSON {
		t.Fatalf("run-start output snapshot = %#v, %v", runSnapshot, err)
	}
}

func TestExecuteKeepsCreateOnceOutputProfileSnapshot(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-output-snapshot")
	script := `return "ok"`
	run, err := runstore.Create("snapshot once", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "", nil)
	first := &profile.Store{Profiles: []profile.Profile{{
		Name: "worker", Harness: profile.HarnessMock, Default: true,
		Output: &workeroutput.Declaration{Dialect: workeroutput.DialectText},
		Taste:  5, Intelligence: 5, Cost: 5,
	}}}
	if _, err := Execute(context.Background(), Options{ScriptSrc: script, Store: first, Run: run, WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	second := &profile.Store{Profiles: []profile.Profile{{
		Name: "worker", Harness: profile.HarnessMock, Default: true,
		Output: &workeroutput.Declaration{Dialect: workeroutput.DialectCodexJSONL},
		Taste:  5, Intelligence: 5, Cost: 5,
	}}}
	if _, err := Execute(context.Background(), Options{ScriptSrc: script, Store: second, Run: run, WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	path, err := runstore.OutputProfilesPath(run.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot workeroutput.RunSnapshot
	if err := json.Unmarshal(b, &snapshot); err != nil || len(snapshot.Profiles) != 1 || snapshot.Profiles[0].Output == nil || snapshot.Profiles[0].Output.Dialect != workeroutput.DialectText {
		t.Fatalf("run-start snapshot = %#v, %v", snapshot, err)
	}
}

func TestClampAgentTimeoutHasThirtyMinuteMinimum(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "short call", in: 10 * time.Minute, want: 30 * time.Minute},
		{name: "minimum", in: 30 * time.Minute, want: 30 * time.Minute},
		{name: "long call", in: 2 * time.Hour, want: 2 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampAgentTimeout(tc.in); got != tc.want {
				t.Fatalf("clampAgentTimeout(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveAgentTimeout(t *testing.T) {
	for _, tc := range []struct {
		name       string
		timeout    time.Duration
		timeoutSet bool
		profile    profile.Profile
		want       time.Duration
	}{
		{name: "default", timeout: defaultAgentTimeout, want: 5 * time.Hour},
		{name: "short explicit timeout", timeout: 10 * time.Minute, timeoutSet: true, want: 30 * time.Minute},
		{
			name:       "long explicit timeout",
			timeout:    6 * time.Hour,
			timeoutSet: true,
			profile:    profile.Profile{TimeoutSec: int((2 * time.Hour) / time.Second)},
			want:       6 * time.Hour,
		},
		{
			name:    "profile timeout without script timeout",
			timeout: defaultAgentTimeout,
			profile: profile.Profile{TimeoutSec: int((2 * time.Hour) / time.Second)},
			want:    2 * time.Hour,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveAgentTimeout(tc.timeout, tc.timeoutSet, tc.profile); got != tc.want {
				t.Fatalf("resolveAgentTimeout(%s, %t, %#v) = %s, want %s", tc.timeout, tc.timeoutSet, tc.profile, got, tc.want)
			}
		})
	}
}

func TestExplicitShortAgentTimeoutIsClamped(t *testing.T) {
	worker := fakeworker.InstallClaude(t, "sleep 0.1; printf finished")
	worker.Name, worker.Default = "slow", true
	store := &profile.Store{Profiles: []profile.Profile{worker}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := Execute(ctx, Options{
		ScriptSrc: `return await agent("finish", {profile: "slow", timeout: 0.01});`,
		Store:     store,
		WorkDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v; a sub-30-minute call timeout must be clamped", err)
	}
	if result != `"finished"` {
		t.Fatalf("Execute() = %s, want %q", result, "finished")
	}
}

func TestExecuteReportsReturnSerializationFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := Execute(ctx, Options{
		ScriptSrc: `const value = {}; value.self = value; return value;`,
		Store:     &profile.Store{},
		WorkDir:   t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "workflow failed:") {
		t.Fatalf("Execute() error = %v, want serialization failure", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Execute() waited for context cancellation instead of reporting serialization failure: %v", err)
	}
}

// A script that never yields stops when its context ends. goja runs it on
// the event loop; without an interrupt, Execute never returns.
func TestExecuteInterruptsABusyScriptOnCancel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Execute(ctx, Options{ScriptSrc: `while (true) {}`, Store: &profile.Store{}, WorkDir: t.TempDir()})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Execute returned no error for an interrupted script")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Execute did not return after its context ended")
	}
}
