package workflowsvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

func refuseAncestry(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, key := range []string{mode.EnvMode, mode.EnvSession, mode.EnvWorkerRun, mode.EnvRoom, mode.EnvRoomID, "DYNA_RUN_ID", "DYNA_AGENT_JOURNAL_ROOT"} {
		t.Setenv(key, "")
	}
	calls, err := websvc.Bind([]websvc.Binding{{Name: "StampPlanAncestry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(context.Context, string, string) (string, error) {
		return "", errors.New("engine refused ancestry")
	}}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
}

func TestExecuteStopsWhenAncestryWriteFails(t *testing.T) {
	refuseAncestry(t)
	t.Setenv("DYNA_RUN_ID", "wf_source")
	source, err := runstore.Create("source", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	source.Finish("ok", "1", nil)
	t.Setenv("DYNA_RUN_ID", "wf_failed")
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
	result, err := executeWithOutbox(context.Background(), Options{ScriptSrc: "return 7", ResumeFrom: source.Meta.ID, WorkDir: t.TempDir()}, profiles, Observer{}, func(string) coordinatorOutbox { return &lifecycleOutbox{} })
	if err == nil || !strings.Contains(err.Error(), "engine refused ancestry") {
		t.Fatalf("execute = %+v,%v", result, err)
	}
	meta, readErr := runstore.ReadMeta(result.ID)
	if readErr != nil || meta.Status != "error" {
		t.Fatalf("failed run metadata = %+v,%v", meta, readErr)
	}
	events, readErr := runstore.ReadEvents(result.ID)
	if readErr != nil || len(events) < 2 || events[len(events)-1].T != "run_end" || events[len(events)-1].Status != "error" {
		t.Fatalf("failed run lifecycle = %#v,%v", events, readErr)
	}

}

func TestLaunchStopsWhenAncestryWriteFails(t *testing.T) {
	refuseAncestry(t)
	_, err := Launch(context.Background(), Options{ScriptSrc: "return 7", ResumeFrom: "wf_source", WorkDir: t.TempDir()}, LaunchOptions{Executable: filepath.Join(t.TempDir(), "must-not-launch")})
	if err == nil || !strings.Contains(err.Error(), "engine refused ancestry") {
		t.Fatalf("launch failed for the wrong cause: %v", err)
	}
}

func TestExecuteGraphFailureOnlyStopsResumedRun(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "fresh"
		if resume {
			name = "resumed"
		}
		t.Run(name, func(t *testing.T) {
			refuseAncestry(t)
			parent := ""
			if resume {
				t.Setenv("DYNA_RUN_ID", "wf_stage_source")
				source, err := runstore.Create("source", "return 1", nil)
				if err != nil {
					t.Fatal(err)
				}
				source.Finish("ok", "1", nil)
				parent = source.Meta.ID
			}
			t.Setenv("DYNA_RUN_ID", "wf_blocked_graph")
			if err := os.MkdirAll(filepath.Join(runstore.RunsDir(), "wf_blocked_graph", "plan.json"), 0700); err != nil {
				t.Fatal(err)
			}
			profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
			warnings := 0
			result, err := executeWithOutbox(context.Background(), Options{ScriptSrc: "return 7", ResumeFrom: parent, WorkDir: t.TempDir()}, profiles, Observer{OnWarning: func(string) { warnings++ }}, func(string) coordinatorOutbox { return &lifecycleOutbox{} })
			if resume {
				if !errors.Is(err, plan.ErrAncestry) || result.Status != "error" {
					t.Fatalf("resumed graph failure = %+v,%v", result, err)
				}
			} else if err != nil || result.Status != "ok" || warnings == 0 {
				t.Fatalf("fresh graph failure = %+v,%v, warnings=%d", result, err, warnings)
			}
		})
	}
}
