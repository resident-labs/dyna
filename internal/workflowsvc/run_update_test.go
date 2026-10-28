package workflowsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/workflow"
)

// schemaOldRunner fails every Start like the old binary does after an update:
// the state it serves was written by a newer dyna.
type schemaOldRunner struct{}

func (schemaOldRunner) Start(context.Context, workflow.CommandSpec) (string, error) {
	return "", fmt.Errorf("start shell supervisor: Error: %s: shells.version is version 6, this dyna knows up to 5", database.ErrNewerSchema)
}
func (schemaOldRunner) Attach(context.Context, string) error { return nil }
func (schemaOldRunner) Wait(context.Context, string) (workflow.CommandExit, error) {
	return workflow.CommandExit{}, errors.New("no shell started")
}
func (schemaOldRunner) Output(string, int64) (string, error) { return "", nil }
func (schemaOldRunner) Stop(string) error                    { return nil }

// captureOutbox records the finish the coordinator sends.
type captureOutbox struct {
	finish []callspec.RunFinish
}

func (o *captureOutbox) CallResultWithinID(context.Context, any, string, ...any) (string, error) {
	return "directory-request", nil
}
func (o *captureOutbox) CallResult(_ context.Context, result any, method string, args ...any) error {
	if method != "FinishRun" {
		return fmt.Errorf("unexpected call: %s", method)
	}
	in := args[0].(callspec.RunFinish)
	o.finish = append(o.finish, in)
	if err := runstore.ForceStatus(in.Run, in.Status, in.Error); err != nil {
		return err
	}
	meta, err := runstore.ReadMeta(in.Run)
	if err == nil {
		*result.(*runstore.Meta) = meta
	}
	return err
}
func (o *captureOutbox) ArmTerminal(context.Context) {}
func (o *captureOutbox) Close(context.Context)       {}

// TestStoppedRunFinishesInterruptedForEngineReport fails first without the
// fix: the run ends in plain error instead of interrupted (D-402).
func TestStoppedRunFinishesInterruptedForEngineReport(t *testing.T) {
	storetest.Isolate(t)
	for _, key := range []string{"CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "MUSE_SESSION_ID", runstore.AgentJournalRootEnv} {
		t.Setenv(key, "")
	}
	t.Setenv("DYNA_RUN_DETACHED", "1")
	runner := CommandRunnerFor
	CommandRunnerFor = func(*runstore.Run, string) (workflow.CommandRunner, error) {
		return schemaOldRunner{}, nil
	}
	t.Cleanup(func() { CommandRunnerFor = runner })
	out := &captureOutbox{}
	warnings := []string{}
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
	result, err := executeWithOutbox(context.Background(), Options{Name: "stopped", ScriptSrc: "await run('prepare'); return 'ok'", WorkDir: t.TempDir()}, profiles,
		Observer{OnWarning: func(message string) { warnings = append(warnings, message) }},
		func(string) coordinatorOutbox { return out })
	if !workflow.StoppedForUpdate(err) {
		t.Fatalf("Execute() error = %v; want the stop-for-update outcome", err)
	}
	if result.Status != runstore.StatusInterrupted {
		t.Fatalf("result status = %s; want interrupted", result.Status)
	}
	if len(out.finish) != 1 || out.finish[0].Status != runstore.StatusInterrupted {
		t.Fatalf("finish calls = %+v; want one interrupted finish", out.finish)
	}
	for _, warning := range warnings {
		if strings.Contains(warning, "completion message") {
			t.Fatalf("the runner posted a completion for a run the engine reports: %s", warning)
		}
	}
}

func TestForegroundRunDoesNotPostCompletion(t *testing.T) {
	storetest.Isolate(t)
	t.Setenv("DYNA_RUN_DETACHED", "0")
	rec, err := room.DefaultStore().Create(room.CreateInput{Name: "foreground"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(mode.EnvRoom, rec.Name)
	t.Setenv(mode.EnvRoomID, rec.ID)
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
	_, err = executeWithOutbox(context.Background(), Options{Name: "foreground", ScriptSrc: "return 'ok'", WorkDir: t.TempDir()}, profiles, Observer{}, func(string) coordinatorOutbox { return &captureOutbox{} })
	if err != nil {
		t.Fatal(err)
	}
	messages, _, err := room.DefaultStore().Log(rec, room.LogOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range messages {
		if _, ok := msg.RunResult(); ok {
			t.Fatal("foreground run posted a completion")
		}
	}
}
