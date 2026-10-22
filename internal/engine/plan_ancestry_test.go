package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestPlanAncestryUsesVerifiedWorkerAndRejectsOtherRun(t *testing.T) {
	dataDir := storetest.DataDir(t)
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	if err := state.MigrateMirrors(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := ancestry.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	t.Setenv("DYNA_WORKER_RUN", "wf_other")
	dir := filepath.Join(dataDir, "runs", "wf_own")
	if err := plan.WriteAtomic(dir, plan.Extract("return 1", plan.Options{}), nil); err != nil {
		t.Fatal(err)
	}
	got, err := stampPlanOnSocket(t, authority.Actor{Role: authority.Worker, Run: "wf_own"}, dir, "wf_parent")
	if err != nil || got != "wf_parent" {
		t.Fatalf("own run = %q, %v", got, err)
	}
	for _, actor := range []authority.Actor{{Role: authority.Worker, Run: "wf_other"}, {}} {
		if _, err := stampPlanOnSocket(t, actor, dir, "wf_wrong"); err == nil {
			t.Fatalf("accepted actor %+v", actor)
		}
	}
	got, err = (ancestry.Store{DataDir: dataDir}).Parent(context.Background(), dir)
	if err != nil || got != "wf_parent" {
		t.Fatalf("row = %q, %v", got, err)
	}
}

func TestPlanAncestryRejectsOutsideAndSymlinkDirectories(t *testing.T) {
	dataDir := storetest.DataDir(t)
	outside := t.TempDir()
	root := filepath.Join(dataDir, "runs")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "wf_link")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{outside, linked, root, "relative/wf_run"} {
		if _, err := stampPlanOnSocket(t, authority.Actor{Role: authority.Owner}, dir, "wf_parent"); err == nil {
			t.Fatalf("accepted %s", dir)
		}
	}
}

func stampPlanOnSocket(t *testing.T, actor authority.Actor, dir, parent string) (string, error) {
	t.Helper()
	calls, err := websvc.Bind([]websvc.Binding{{
		Name: "StampPlanAncestry", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(ctx context.Context, dir, parent string) (string, error) {
			return (services{}).stampPlanAncestry(authority.WithActor(ctx, actor), dir, parent)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	var got string
	err = engineclient.New("").Call(context.Background(), "StampPlanAncestry", []any{dir, parent}, &got)
	return got, err
}

func TestPlanAncestryRejectsSymlinkIntoAnotherRun(t *testing.T) {
	dataDir := storetest.DataDir(t)
	if err := ancestry.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	own := filepath.Join(dataDir, "runs", "wf_own")
	victim := filepath.Join(dataDir, "runs", "wf_other")
	if err := os.MkdirAll(own, 0700); err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteAtomic(victim, plan.Extract("return 1", plan.Options{}), nil); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(own, "alias")
	if err := os.Symlink(victim, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := stampPlanOnSocket(t, authority.Actor{Role: authority.Worker, Run: "wf_own"}, alias, "wf_wrong"); err == nil {
		t.Fatal("worker stamped another run through an alias")
	}
	parent, err := (ancestry.Store{DataDir: dataDir}).Parent(context.Background(), victim)
	if err != nil || parent != "" {
		t.Fatalf("other run ancestry changed: %q,%v", parent, err)
	}
}
