package workflow

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

func TestNestedWorkflowStopsWhenAncestryWriteFails(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_nested_failure")
	s := enginetest.ServePlanAncestry(t)
	dir := t.TempDir()
	writeWorkflowScript(t, dir, "child.js", "return 42")
	src := "return await workflow('./child.js')"
	run, err := runstore.Create("parent", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	opts := plan.Options{SourcePath: filepath.Join(dir, "root.js"), WorkDir: dir, WriteAncestry: plan.StampAncestry, ResumedFrom: "wf_source"}
	if _, err := plan.Ensure(run.Dir, src, opts); err != nil {
		t.Fatal(err)
	}
	calls, err := websvc.Bind([]websvc.Binding{{Name: "StampPlanAncestry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, path, parent string) (string, error) {
		if strings.Contains(path, string(filepath.Separator)+"workflows"+string(filepath.Separator)) {
			return "", errors.New("engine refused nested ancestry")
		}
		return s.Stamp(ctx, path, parent)
	}}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	_, err = executeWorkflowTest(t, src, Options{ScriptPath: opts.SourcePath, WorkDir: dir, Store: mockWorkflowStore(0, 0), Run: run, ResumeFrom: "wf_source"})
	if err == nil || !strings.Contains(err.Error(), "engine refused nested ancestry") {
		t.Fatalf("nested workflow = %v", err)
	}
}
