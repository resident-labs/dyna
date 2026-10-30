package workflowsvc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
	"testing"

	"dyna-agent/internal/runstore"
)

func TestReplayEligibility(t *testing.T) {
	inputs := runstore.WorkflowReplayInputs{Deterministic: true, Fingerprint: "one"}
	manifest := runstore.WorkflowManifest{Complete: true, Calls: []runstore.WorkflowManifestCall{{Result: json.RawMessage(`null`)}}}
	if reason := replayEligibility(inputs, manifest, "one"); reason != ReplayEligible {
		t.Fatalf("eligible = %s", reason)
	}
	if reason := replayEligibility(inputs, manifest, "two"); reason != ReplayInputMismatch {
		t.Fatalf("mismatch = %s", reason)
	}
	inputs.Deterministic = false
	if reason := replayEligibility(inputs, manifest, "one"); reason != ReplayNotDeterministic {
		t.Fatalf("declaration = %s", reason)
	}
	inputs.Deterministic = true
	manifest.Complete = false
	manifest.Calls[0].Result = nil
	if reason := replayEligibility(inputs, manifest, "one"); reason != ReplayUncertain {
		t.Fatalf("uncertain = %s", reason)
	}
	manifest.Calls[0].Result = json.RawMessage(`null`)
	if reason := replayEligibility(inputs, manifest, "one"); reason != ReplayIncomplete {
		t.Fatalf("incomplete = %s", reason)
	}
}

func TestAutomaticReplayLaunchesEligibleRunOnlyOnce(t *testing.T) {
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "")
	t.Setenv("DYNA_RUN_DETACHED", "1")
	t.Setenv("REPLAY_TEST_INPUT", "one")
	t.Setenv("DYNA_ROOM_ID", "room_test")
	t.Setenv("DYNA_ROOM", "test-room")
	t.Cleanup(func() { database.Close(store.DataDir()) })
	dir := replayTestWorktree(t)
	profiles := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
	if err := profiles.Save(); err != nil {
		t.Fatal(err)
	}
	profiles, err := profile.Load(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{ScriptSrc: `const meta = {deterministic: true}; return 1`, Name: "test", WorkDir: dir}
	run, err := runstore.Create(opts.Name, opts.ScriptSrc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.BeginWorkflowManifest(t.Context(), run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := runstore.SealWorkflowManifest(t.Context(), run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	head, err := replayWorktreeHead(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	roomEnv := RoomRunEnvironment(authority.Actor{Role: authority.Owner}, room.Record{ID: "room_test", Name: "test-room"})
	roomEnv = append(roomEnv, "DYNA_RUN_DETACHED=1")
	hash, _, err := replayFingerprint(opts, profiles, roomEnv, head)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.RecordWorkflowReplayInputs(t.Context(), run.Meta.ID, runstore.WorkflowReplayInputs{Deterministic: true, Fingerprint: hash, Options: data}); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(runstore.StatusInterrupted, "", nil); err != nil {
		t.Fatal(err)
	}
	old := autoReplayLaunch
	defer func() { autoReplayLaunch = old }()
	launches := 0
	autoReplayLaunch = func(_ context.Context, got Options, launch LaunchOptions) (string, error) {
		launches++
		if !slices.Contains(launch.Env, "DYNA_ROOM_ID=room_test") || slices.Contains(launch.Env, "DYNA_DATA_DIR="+os.Getenv("DYNA_DATA_DIR")) {
			t.Fatalf("launch did not restore room environment")
		}
		if !got.SafeReplay || got.ResumeFrom != run.Meta.ID {
			t.Fatalf("launch options = %+v", got)
		}
		return "wf_new", nil
	}
	if err := os.Setenv("REPLAY_TEST_INPUT", "two"); err != nil {
		t.Fatal(err)
	}
	if _, reason, err := TryAutomaticReplay(t.Context(), run.Meta.ID); err != nil || reason != ReplayInputMismatch || launches != 0 {
		t.Fatalf("changed input = %s, %v, launches %d", reason, err, launches)
	}
	if err := os.Setenv("REPLAY_TEST_INPUT", "one"); err != nil {
		t.Fatal(err)
	}
	id, reason, err := TryAutomaticReplay(t.Context(), run.Meta.ID)
	if err != nil || id != "wf_new" || reason != ReplayEligible {
		t.Fatalf("first = %q %s %v", id, reason, err)
	}
	_, reason, err = TryAutomaticReplay(t.Context(), run.Meta.ID)
	if err != nil || reason != ReplayLaunchUncertain || launches != 1 {
		t.Fatalf("second = %s %v, launches %d", reason, err, launches)
	}
}

func replayTestWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	return dir
}

func TestSafeReplayExecutesVerifiedManifest(t *testing.T) {
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "")
	t.Setenv("DYNA_RUN_DETACHED", "0")
	t.Cleanup(func() { database.Close(store.DataDir()) })
	dir := replayTestWorktree(t)
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
	calls, err := websvc.Bind([]websvc.Binding{{Name: "StampPlanAncestry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, _, parent string) (string, error) { return parent, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	opts := Options{ScriptSrc: `const meta = {deterministic: true}; return 42`, Name: "safe", WorkDir: dir}
	factory := func(string) coordinatorOutbox { return &lifecycleOutbox{} }
	first, err := executeWithOutbox(t.Context(), opts, profiles, Observer{}, factory)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.ForceStatus(first.ID, runstore.StatusInterrupted, "update"); err != nil {
		t.Fatal(err)
	}
	opts.ResumeFrom, opts.SafeReplay = first.ID, true
	second, err := executeWithOutbox(t.Context(), opts, profiles, Observer{}, factory)
	if err != nil || second.Status != "ok" || second.Value != "42" {
		t.Fatalf("safe replay = %+v, %v", second, err)
	}
}

func TestFailedReplayBeforeExecutionCannotSealEmptyManifest(t *testing.T) {
	t.Setenv("DYNA_DATA_DIR", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "")
	t.Setenv("DYNA_RUN_DETACHED", "0")
	t.Cleanup(func() { database.Close(store.DataDir()) })
	dir := replayTestWorktree(t)
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
	calls, err := websvc.Bind([]websvc.Binding{{Name: "StampPlanAncestry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(context.Context, string, string) (string, error) { return "", errors.New("refused") }}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	opts := Options{ScriptSrc: `const meta = {deterministic: true}; return 42`, Name: "safe", WorkDir: dir}
	factory := func(string) coordinatorOutbox { return &lifecycleOutbox{} }
	first, err := executeWithOutbox(t.Context(), opts, profiles, Observer{}, factory)
	if err != nil {
		t.Fatal(err)
	}
	opts.ResumeFrom, opts.SafeReplay = first.ID, true
	second, err := executeWithOutbox(t.Context(), opts, profiles, Observer{}, factory)
	if err == nil {
		t.Fatal("expected ancestry failure")
	}
	manifest, err := runstore.ReadWorkflowManifest(t.Context(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Complete || manifest.UnsafeReason == "" {
		t.Fatalf("failed replay manifest = %+v", manifest)
	}
}
