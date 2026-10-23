package workflowsvc

import (
	"context"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store/storetest"
)

func TestExecuteFinalizesEveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, src, status string
		cancel            bool
	}{
		{name: "success", src: "return {answer:42}", status: "ok"},
		{name: "error", src: "throw new Error('failed')", status: "error"},
		{name: "canceled", src: "return 1", status: "canceled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storetest.Isolate(t)
			for _, key := range []string{"CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "MUSE_SESSION_ID", runstore.AgentJournalRootEnv} {
				t.Setenv(key, "")
			}
			profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observer := Observer{}
			if tc.cancel {
				observer.OnStart = func(runstore.Meta) { cancel() }
			}
			result, err := executeWithOutbox(ctx, Options{Name: tc.name, ScriptSrc: tc.src, WorkDir: t.TempDir()}, profiles, observer, func(string) coordinatorOutbox { return &lifecycleOutbox{} })
			if (err != nil) != (tc.status != "ok") {
				t.Fatalf("result = %+v, %v", result, err)
			}
			meta, err := runstore.ReadMeta(result.ID)
			if err != nil || meta.Status != tc.status {
				t.Fatalf("metadata = %+v, %v", meta, err)
			}
			events, err := runstore.ReadEvents(result.ID)
			if err != nil || len(events) < 2 || events[0].T != "run_start" || events[len(events)-1].T != "run_end" || events[len(events)-1].Status != tc.status {
				t.Fatalf("events = %+v, %v", events, err)
			}
		})
	}
}
