package runstore

import (
	"testing"
)

// A starting row protects a staged launch until its launcher or worker ends
// it; a row the worker already replaced keeps its own lifecycle.
func TestStartingRunFinishesOnlyWhileStarting(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := RecordStartingRun(Meta{ID: "wf_starting", Name: "staged"}); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMeta("wf_starting"); err != nil || m.Status != "running" || !m.Starting {
		t.Fatalf("starting row = %+v, %v", m, err)
	}
	if err := FinishStartingRun("wf_starting", "worker exited before the run started"); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMeta("wf_starting"); err != nil || m.Status != "error" || m.Starting || m.EndedAt.IsZero() {
		t.Fatalf("finished starting row = %+v, %v", m, err)
	}

	// The worker's create replaces the row; a late finish cannot end the run.
	if err := RecordStartingRun(Meta{ID: "wf_replaced", Name: "replaced"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_RUN_ID", "wf_replaced")
	t.Setenv(SessionEnv, "")
	if _, err := Create("replaced", "return 1", nil); err != nil {
		t.Fatal(err)
	}
	if err := FinishStartingRun("wf_replaced", "late"); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMeta("wf_replaced"); err != nil || m.Status != "running" || m.Starting || m.Pid == 0 {
		t.Fatalf("replaced row = %+v, %v", m, err)
	}
}
