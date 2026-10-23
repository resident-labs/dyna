package workflowsvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store/storetest"
)

type lifecycleOutbox struct {
	finishErr error
	terminal  context.Context
	closed    bool
}

func (o *lifecycleOutbox) CallResultWithinID(context.Context, any, string, ...any) (string, error) {
	return "directory-request", nil
}
func (o *lifecycleOutbox) CallResult(_ context.Context, result any, method string, args ...any) error {
	if method != "FinishRun" {
		return errors.New("unexpected call: " + method)
	}
	if o.finishErr != nil {
		return o.finishErr
	}
	in := args[0].(callspec.RunFinish)
	if err := runstore.ForceStatus(in.Run, in.Status, in.Error); err != nil {
		return err
	}
	meta, err := runstore.ReadMeta(in.Run)
	if err == nil {
		*result.(*runstore.Meta) = meta
	}
	return err
}
func (o *lifecycleOutbox) ArmTerminal(ctx context.Context) { o.terminal = ctx }
func (o *lifecycleOutbox) Close(ctx context.Context)       { o.closed = ctx == o.terminal }

func TestCoordinatorFailureDoesNotPublishSuccessfulCompletion(t *testing.T) {
	storetest.Isolate(t)
	for _, key := range []string{"CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "MUSE_SESSION_ID", runstore.AgentJournalRootEnv} {
		t.Setenv(key, "")
	}
	t.Setenv("DYNA_RUN_DETACHED", "1")
	refused := errors.New("finish refused")
	out := &lifecycleOutbox{finishErr: refused}
	warnings := []string{}
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
	result, err := executeWithOutbox(context.Background(), Options{Name: "refused", ScriptSrc: "return 42", WorkDir: t.TempDir()}, profiles, Observer{
		OnWarning: func(message string) { warnings = append(warnings, message) },
	}, func(string) coordinatorOutbox { return out })
	if !errors.Is(err, refused) || result.Status != "error" {
		t.Fatalf("result = %+v, error = %v; want refused finish", result, err)
	}
	meta, err := runstore.ReadMeta(result.ID)
	if err != nil || meta.Status != "running" || !meta.EndedAt.IsZero() {
		t.Fatalf("unacknowledged metadata = %+v, %v", meta, err)
	}
	events, err := runstore.ReadEvents(result.ID)
	if err != nil || len(events) == 0 || events[len(events)-1].Status != "error" {
		t.Fatalf("events = %+v, %v", events, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected completion attempt or warning: %v", warnings)
	}
	if out.terminal == nil || !out.closed {
		t.Fatal("finish and close did not share the terminal scope")
	}
	t.Run("result artifact failure still finishes", func(t *testing.T) {
		run, err := runstore.Create("artifact failure", "return 42", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(run.Dir, "result.json"), 0700); err != nil {
			t.Fatal(err)
		}
		var received callspec.RunFinish
		run.SetWriters(nil, func(in callspec.RunFinish) (runstore.Meta, error) {
			received = in
			meta := run.Meta
			meta.Status, meta.Error, meta.EndedAt = in.Status, in.Error, in.EndedAt
			return meta, nil
		})
		err = run.Finish("ok", "42", nil)
		if err == nil || !strings.Contains(err.Error(), "write run result") {
			t.Fatalf("Finish error = %v; want artifact error", err)
		}
		if received.Status != "error" || !strings.Contains(received.Error, "write run result") || received.EndedAt.IsZero() {
			t.Fatalf("finish request = %+v; want acknowledged error outcome", received)
		}
		if run.Meta.Status != "error" || run.Meta.EndedAt.IsZero() {
			t.Fatalf("metadata = %+v; want terminal error", run.Meta)
		}
	})

}
