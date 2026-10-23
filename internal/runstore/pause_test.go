package runstore

import (
	"os"
	"testing"
)

// The paused flag is a field of the run's row: SetPaused writes it, the
// runner and every listing read it, and the end of the run clears it.
func TestPausedLivesInTheRunRow(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_pause-row")
	run, err := Create("pause", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetPaused(run.Meta.ID, true); err != nil {
		t.Fatal(err)
	}
	// TODO: legacy/shim remove later — keep the paused file for an earlier-release runner; remove with the paused-file copy (item 31).
	if _, err := os.Stat(pausePath(run.Meta.ID)); err != nil {
		t.Fatalf("the earlier release's paused file is missing: %v", err)
	}
	m, err := ReadMeta(run.Meta.ID)
	if err != nil || !m.Paused || !IsPaused(run.Meta.ID) {
		t.Fatalf("after pause: meta paused %v, IsPaused %v, err %v", m.Paused, IsPaused(run.Meta.ID), err)
	}
	if err := SetPaused(run.Meta.ID, false); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMeta(run.Meta.ID); err != nil || m.Paused || IsPaused(run.Meta.ID) {
		t.Fatalf("after resume: meta paused %v, err %v", m.Paused, err)
	}
	if err := SetPaused(run.Meta.ID, true); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "1", nil)
	if m, err := ReadMeta(run.Meta.ID); err != nil || m.Paused || IsPaused(run.Meta.ID) {
		t.Fatalf("a finished run stays paused: %v %v", m.Paused, err)
	}
	if _, err := os.Stat(pausePath(run.Meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("finish left the paused file: %v", err)
	}
	if err := SetPaused(run.Meta.ID, true); err != nil {
		t.Fatal(err)
	}
	if IsPaused(run.Meta.ID) {
		t.Fatal("a finished run was paused")
	}
	if _, err := os.Stat(pausePath(run.Meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("a finished run gained a paused file: %v", err)
	}
	// TODO: legacy/shim remove later — resume can clear a stale earlier-release pause file on a finished run; remove with the paused-file copy (item 31).
	if err := os.WriteFile(pausePath(run.Meta.ID), []byte("paused\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsPaused(run.Meta.ID) {
		t.Fatal("a stale file paused a finished run")
	}
	if err := SetPaused(run.Meta.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pausePath(run.Meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("resume left a finished run's stale paused file: %v", err)
	}
}

func TestForceStatusClearsPausedFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_pause-force-status")
	run, err := Create("pause", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetPaused(run.Meta.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := ForceStatus(run.Meta.ID, "error", "process lost"); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMeta(run.Meta.ID); err != nil || m.Status != "error" || m.Paused || IsPaused(run.Meta.ID) {
		t.Fatalf("after force status: meta %+v, err %v", m, err)
	}
	if _, err := os.Stat(pausePath(run.Meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("force status left the paused file: %v", err)
	}
}

// Pause files cannot overwrite canonical state after startup import.
func TestIsPausedIgnoresStalePausedFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_pause-legacy")
	run, err := Create("pause", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pausePath(run.Meta.ID), []byte("paused\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMeta(run.Meta.ID); err != nil || m.Paused {
		t.Fatalf("want file-only pause: meta %+v, err %v", m, err)
	}
	if IsPaused(run.Meta.ID) {
		t.Fatal("a stale file overwrote canonical pause state")
	}
	if err := SetPaused(run.Meta.ID, false); err != nil {
		t.Fatal(err)
	}
	if IsPaused(run.Meta.ID) {
		t.Fatal("resume left the file-only pause active")
	}
}
