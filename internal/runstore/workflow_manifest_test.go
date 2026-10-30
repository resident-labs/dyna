package runstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestWorkflowManifestSealsOnlyAfterRecordedResults(t *testing.T) {
	run := newTestRun(t, "wf_manifest")
	ctx := context.Background()
	if err := BeginWorkflowManifest(ctx, run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := RegisterWorkflowCall(ctx, run.Meta.ID, WorkflowCallAgent, "identity", 1); err != nil {
		t.Fatal(err)
	}
	if err := SealWorkflowManifest(ctx, run.Meta.ID); err == nil {
		t.Fatal("sealed an unfinished call")
	}
	if err := CompleteWorkflowCall(ctx, run.Meta.ID, WorkflowCallAgent, "identity", 1, "session", "answer", nil); err != nil {
		t.Fatal(err)
	}
	if err := SealWorkflowManifest(ctx, run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadWorkflowManifest(ctx, run.Meta.ID)
	if err != nil || !manifest.Complete || len(manifest.Calls) != 1 || manifest.Calls[0].ExternalID != "session" || string(manifest.Calls[0].Result) != `"answer"` {
		t.Fatalf("manifest = %+v, %v", manifest, err)
	}
	if err := RegisterWorkflowCall(ctx, run.Meta.ID, WorkflowCallPost, "later", 2); err == nil {
		t.Fatal("accepted a call after sealing")
	}
}

func TestWorkflowManifestUnsafeAndDeletion(t *testing.T) {
	run := newTestRun(t, "wf_manifest_unsafe")
	ctx := context.Background()
	if err := BeginWorkflowManifest(ctx, run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := MarkWorkflowManifestUnsafe(ctx, run.Meta.ID, "unrecorded job"); err != nil {
		t.Fatal(err)
	}
	if err := SealWorkflowManifest(ctx, run.Meta.ID); !errors.Is(err, ErrWorkflowManifestIncomplete) {
		t.Fatalf("seal = %v", err)
	}
	manifest, err := ReadWorkflowManifest(ctx, run.Meta.ID)
	if err != nil || manifest.Complete || manifest.UnsafeReason != "unrecorded job" {
		t.Fatalf("manifest = %+v, %v", manifest, err)
	}
	if err := run.Finish("ok", "null", nil); err != nil {
		t.Fatal(err)
	}
	if err := Remove(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorkflowManifest(ctx, run.Meta.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted manifest: %v", err)
	}
}
