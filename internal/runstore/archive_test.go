package runstore

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/workeroutput"
)

// archiveFixture creates a finished run with every artifact kind a reader
// might resolve by id: meta, events, journal, result, plan, and per-agent
// journal/output files.
func archiveFixture(t *testing.T, id string) *Run {
	t.Helper()
	t.Setenv("DYNA_RUN_ID", id)
	run, err := Create("archive "+id, "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Append(Event{T: "run_start", Title: id})
	run.Journal(JournalEntry{ID: 1, Label: "a", Profile: "mock", Key: "k1", Prompt: "p", Result: "r"})
	if _, err := run.StartAgentJournal(1, "a", "mock", "", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartAgentOutput(1); err != nil {
		t.Fatal(err)
	}
	selected := workeroutput.ProfileSnapshot{Name: "mock", Output: &workeroutput.Declaration{Dialect: workeroutput.DialectText}}
	normalized, err := run.StartAgentNormalizedOutput(1, selected)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(normalized, []byte(`{"v":1,"kind":"message","role":"assistant","text":"hi"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Dir, "plan.json"), []byte(`{"version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", `{"answer":1}`, nil)
	return run
}

func manifestOf(t *testing.T, dir string) map[string]manifestEntry {
	t.Helper()
	m, err := treeManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func assertResolvable(t *testing.T, id string) {
	t.Helper()
	meta, err := ReadMeta(id)
	if err != nil || meta.ID != id || meta.Status != "ok" {
		t.Fatalf("ReadMeta(%s) = %#v, %v", id, meta, err)
	}
	if events, err := ReadEvents(id); err != nil || len(events) == 0 {
		t.Fatalf("ReadEvents = %d, %v", len(events), err)
	}
	if events, _, err := ReadEventsFrom(id, 0); err != nil || len(events) == 0 {
		t.Fatalf("ReadEventsFrom = %d, %v", len(events), err)
	}
	if journal, err := ReadJournal(id); err != nil || len(journal) != 1 || journal[0].Key != "k1" {
		t.Fatalf("ReadJournal = %#v, %v", journal, err)
	}
	if journal, _, err := ReadJournalFrom(id, 0); err != nil || len(journal) != 1 {
		t.Fatalf("ReadJournalFrom = %#v, %v", journal, err)
	}
	if result, ok := ReadResult(id); !ok || !strings.Contains(result, "answer") {
		t.Fatalf("ReadResult = %q, %t", result, ok)
	}
	if entries, _, err := ReadAgentJournalFrom(id, 1, 0); err != nil || len(entries) != 1 || entries[0].Kind != "start" {
		t.Fatalf("ReadAgentJournalFrom = %#v, %v", entries, err)
	}
	if events, _, err := ReadAgentNormalizedOutputFrom(id, 1, 0); err != nil || len(events) != 1 {
		t.Fatalf("ReadAgentNormalizedOutputFrom = %#v, %v", events, err)
	}
	if _, _, err := ReadAgentOutputFrom(id, 1, 0); err != nil {
		t.Fatalf("ReadAgentOutputFrom: %v", err)
	}
	f, canon, err := OpenConfinedRunFile(id, "plan.json")
	if err != nil || canon != id {
		t.Fatalf("OpenConfinedRunFile = %q, %v", canon, err)
	}
	f.Close()
	if canon, err := CanonicalRunID(id); err != nil || canon != id {
		t.Fatalf("CanonicalRunID = %q, %v", canon, err)
	}
	if p, err := ConfinedRunPath(id, "journal.jsonl"); err != nil {
		t.Fatalf("ConfinedRunPath: %v", err)
	} else if _, err := os.Stat(p); err != nil {
		t.Fatalf("ConfinedRunPath result %s: %v", p, err)
	}
}

func TestArchiveRunMovesByteIdenticalAndStaysResolvable(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv(ArchiveDirEnv, "")
	run := archiveFixture(t, "wf_archive-move")
	before := manifestOf(t, run.Dir)

	res, err := ArchiveRun(run.Meta.ID)
	if err != nil {
		t.Fatalf("ArchiveRun = %#v, %v", res, err)
	}
	wantDir := filepath.Join(data, "dyna", "archive", "runs", run.Meta.ID)
	if res.Dir != wantDir || RunDir(run.Meta.ID) != wantDir || !IsArchived(run.Meta.ID) {
		t.Fatalf("archive dir = %s, RunDir = %s", res.Dir, RunDir(run.Meta.ID))
	}
	if after := manifestOf(t, wantDir); manifestDiff(before, after) != "" {
		t.Fatalf("archived tree differs: %s", manifestDiff(before, after))
	}

	// Archiving leaves no runs/<id> pointer: RunDir resolves the archived id, and
	// no Dyna code reads the live path.
	if _, err := os.Lstat(filepath.Join(RunsDir(), run.Meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("archived run left a live entry: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(wantDir, "agents", "1", "journal.jsonl")); err != nil || !strings.Contains(string(b), "Agent started") {
		t.Fatalf("read of an archived artifact: %v", err)
	}

	live, _ := List()
	if len(live) != 0 {
		t.Fatalf("List still shows archived run: %#v", live)
	}
	archived, _ := ListArchived()
	all, _ := ListAll()
	if len(archived) != 1 || archived[0].ID != run.Meta.ID || len(all) != 1 {
		t.Fatalf("ListArchived = %#v, ListAll = %#v", archived, all)
	}
	assertResolvable(t, run.Meta.ID)

	if _, err := ArchiveRun(run.Meta.ID); err == nil || !strings.Contains(err.Error(), "already archived") {
		t.Fatalf("second archive err = %v", err)
	}
}

// An unarchive still removes a runs/<id> pointer that an older release left,
// so the restored run is a real directory again.
func TestUnarchiveRemovesAnOlderReleasePointer(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(ArchiveDirEnv, "")
	run := archiveFixture(t, "wf_archive-old-pointer")
	if _, err := ArchiveRun(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ArchiveDir(), run.Meta.ID), run.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := UnarchiveRun(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(run.Dir); err != nil || !info.IsDir() {
		t.Fatalf("live dir not restored: %v", err)
	}
	assertResolvable(t, run.Meta.ID)
}

// An archive root outside the data directory resolves by id as well, and the
// tree moves there byte for byte.
func TestArchiveOutsideDataDirResolvesByID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	archiveRoot := filepath.Join(t.TempDir(), "archive")
	t.Setenv(ArchiveDirEnv, archiveRoot)
	run := archiveFixture(t, "wf_archive-abs")
	before := manifestOf(t, run.Dir)
	if _, err := ArchiveRun(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(RunsDir(), run.Meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("archived run left a live entry: %v", err)
	}
	dest := filepath.Join(archiveRoot, run.Meta.ID)
	if RunDir(run.Meta.ID) != dest {
		t.Fatalf("RunDir = %s; want %s", RunDir(run.Meta.ID), dest)
	}
	if diff := manifestDiff(before, manifestOf(t, dest)); diff != "" {
		t.Fatalf("archived tree differs: %s", diff)
	}
	assertResolvable(t, run.Meta.ID)
}

func TestArchiveRefusesActiveRuns(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(ArchiveDirEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_archive-running")
	running, err := Create("running", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { running.Finish("ok", "", nil) })
	if _, err := ArchiveRun(running.Meta.ID); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("running archive err = %v", err)
	}
	paused := archiveFixture(t, "wf_archive-paused")
	// TODO: legacy/shim remove later — an earlier release could leave a paused file on a finished run; remove with the archiver's paused-file check (item 31).
	if err := os.WriteFile(pausePath(paused.Meta.ID), []byte("paused\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveRun(paused.Meta.ID); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("paused archive err = %v", err)
	}
	for _, id := range []string{running.Meta.ID, paused.Meta.ID} {
		if info, err := os.Lstat(filepath.Join(RunsDir(), id)); err != nil || !info.IsDir() {
			t.Fatalf("%s moved: %v", id, err)
		}
	}
}

func TestUnarchiveRestoresLiveDirectory(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(ArchiveDirEnv, "")
	run := archiveFixture(t, "wf_archive-restore")
	before := manifestOf(t, run.Dir)
	if _, err := ArchiveRun(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := UnarchiveRun(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(run.Dir)
	if err != nil || !info.IsDir() || IsArchived(run.Meta.ID) {
		t.Fatalf("live dir not restored: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(ArchiveDir(), run.Meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("archive copy left behind: %v", err)
	}
	if diff := manifestDiff(before, manifestOf(t, run.Dir)); diff != "" {
		t.Fatalf("restored tree differs: %s", diff)
	}
	if live, _ := List(); len(live) != 1 {
		t.Fatalf("List after unarchive = %#v", live)
	}
	if _, err := UnarchiveRun(run.Meta.ID); err == nil || !strings.Contains(err.Error(), "not archived") {
		t.Fatalf("second unarchive err = %v", err)
	}
}

func TestArchiveCrossFilesystemCopiesVerifiesAndRemoves(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(ArchiveDirEnv, "")
	run := archiveFixture(t, "wf_archive-exdev")
	if err := os.Symlink("journal.jsonl", filepath.Join(run.Dir, "journal-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(run.Dir, "result.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(run.Dir, "script.js"), old, old); err != nil {
		t.Fatal(err)
	}
	before := manifestOf(t, run.Dir)
	calls := 0
	renameDir = func(src, dst string) error {
		calls++
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { renameDir = os.Rename })

	if _, err := ArchiveRun(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("rename attempts = %d", calls)
	}
	dest := filepath.Join(ArchiveDir(), run.Meta.ID)
	if diff := manifestDiff(before, manifestOf(t, dest)); diff != "" {
		t.Fatalf("cross-filesystem copy differs: %s", diff)
	}
	if info, err := os.Stat(filepath.Join(dest, "script.js")); err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("mtime not preserved: %v", err)
	}
	entries, _ := os.ReadDir(RunsDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("staging residue in runs dir: %s", e.Name())
		}
	}
	archEntries, _ := os.ReadDir(ArchiveDir())
	if len(archEntries) != 1 {
		t.Fatalf("archive dir entries = %v", archEntries)
	}
	assertResolvable(t, run.Meta.ID)
}

func TestRemoveDeletesArchivedRunAndPointer(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(ArchiveDirEnv, "")
	run := archiveFixture(t, "wf_archive-remove")
	if _, err := ArchiveRun(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	// A pointer an older release left goes with the archived tree.
	_ = os.Remove(run.Dir)
	if err := os.Symlink(filepath.Join(ArchiveDir(), run.Meta.ID), run.Dir); err != nil {
		t.Fatal(err)
	}
	if err := Remove(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{run.Dir, filepath.Join(ArchiveDir(), run.Meta.ID)} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived remove: %v", p, err)
		}
	}
}

// Retention never unlinks a live entry that is not this archive's pointer: a
// foreign link, its target and the archive all stay, and the canonical row is
// still there.
func TestRemoveKeepsAForeignLiveSymlink(t *testing.T) {
	for _, tc := range []struct{ name, target string }{
		{"foreign", "outside"},
		{"dangling", "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv(ArchiveDirEnv, "")
			run := archiveFixture(t, "wf_archive-foreign")
			if _, err := ArchiveRun(run.Meta.ID); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), tc.target)
			if tc.target == "outside" {
				if err := os.MkdirAll(outside, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, run.Dir); err != nil {
				t.Fatal(err)
			}
			if err := Remove(run.Meta.ID); err == nil {
				t.Fatal("remove unlinked a live entry that is not the archive's pointer")
			}
			if info, err := os.Lstat(run.Dir); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the live entry was not kept: %v, %v", info, err)
			}
			if tc.target == "outside" {
				if _, err := os.Stat(outside); err != nil {
					t.Fatalf("the link target was removed: %v", err)
				}
			}
			if _, err := os.Stat(filepath.Join(ArchiveDir(), run.Meta.ID)); err != nil {
				t.Fatalf("the archive was removed: %v", err)
			}
			if _, err := ReadMeta(run.Meta.ID); err != nil {
				t.Fatalf("the canonical row was removed: %v", err)
			}
		})
	}
}

// writeMetaRun writes a minimal run directory with explicit timestamps.
func writeMetaRun(t *testing.T, id, status string, ended time.Time, resumedFrom string) {
	t.Helper()
	dir := filepath.Join(RunsDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := Meta{ID: id, Name: id, Status: status, StartedAt: ended.Add(-time.Minute)}
	if status != "running" {
		meta.EndedAt = ended
	} else {
		meta.Pid = os.Getpid()
	}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	header := map[string]any{"version": 1}
	if resumedFrom != "" {
		header["resumedFrom"] = resumedFrom
	}
	b, _ = json.Marshal(header)
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if resumedFrom != "" {
		dataDir := store.DataDir()
		if err := state.MigrateMirrors(dataDir); err != nil {
			t.Fatal(err)
		}
		if err := ancestry.Migrate(dataDir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { database.Close(dataDir) })
		if _, err := (ancestry.Store{DataDir: dataDir}).Stamp(context.Background(), dir, resumedFrom); err != nil {
			t.Fatal(err)
		}
	}

}

func ids(metas []Meta) []string {
	out := make([]string, len(metas))
	for i, m := range metas {
		out[i] = m.ID
	}
	slices.Sort(out)
	return out
}

func TestPlanArchiveSelectionAndLineageProtection(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(ArchiveDirEnv, "")
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	old := now.Add(-72 * time.Hour)
	recent := now.Add(-2 * time.Hour)

	writeMetaRun(t, "wf_old-ok", "ok", old, "")
	writeMetaRun(t, "wf_old-canceled", "canceled", old, "")
	writeMetaRun(t, "wf_old-error", "error", old, "")
	writeMetaRun(t, "wf_recent-ok", "ok", recent, "")
	// An active resume chain: root <- mid <- running.
	writeMetaRun(t, "wf_chain-root", "ok", old, "")
	writeMetaRun(t, "wf_chain-mid", "error", old, "wf_chain-root")
	writeMetaRun(t, "wf_chain-running", "running", now, "wf_chain-mid")
	// A finished recent resume of an old run keeps its ancestor live.
	writeMetaRun(t, "wf_lineage-old", "ok", old, "")
	writeMetaRun(t, "wf_lineage-recent", "ok", recent, "wf_lineage-old")
	// Paused and still-starting runs.
	writeMetaRun(t, "wf_paused", "running", old, "")
	if err := SetPaused("wf_paused", true); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(RunsDir(), "wf_starting"), 0o755); err != nil {
		t.Fatal(err)
	}

	sel, skips, err := PlanArchive(ArchiveSelector{OlderThan: 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(sel), []string{"wf_old-canceled", "wf_old-error", "wf_old-ok"}; !slices.Equal(got, want) {
		t.Fatalf("--older-than 24h selected %v, want %v (skips %#v)", got, want, skips)
	}
	if !slices.ContainsFunc(skips, func(s ArchiveSkip) bool {
		return s.ID == "wf_lineage-old" && strings.Contains(s.Reason, "wf_lineage-recent")
	}) {
		t.Fatalf("lineage skip missing: %#v", skips)
	}

	sel, _, err = PlanArchive(ArchiveSelector{AllFinished: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(sel); slices.Contains(got, "wf_chain-root") || slices.Contains(got, "wf_chain-mid") ||
		slices.Contains(got, "wf_chain-running") || slices.Contains(got, "wf_paused") || slices.Contains(got, "wf_starting") {
		t.Fatalf("--all-finished selected a protected run: %v", got)
	}

	sel, _, err = PlanArchive(ArchiveSelector{Statuses: []string{"canceled", "error"}, OlderThan: 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(sel), []string{"wf_old-canceled", "wf_old-error"}; !slices.Equal(got, want) {
		t.Fatalf("--status selected %v, want %v", got, want)
	}

	sel, skips, err = PlanArchive(ArchiveSelector{IDs: []string{"wf_chain-root", "wf_paused", "wf_recent-ok", "wf_missing"}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(sel); !slices.Equal(got, []string{"wf_recent-ok"}) {
		t.Fatalf("explicit ids selected %v (skips %#v)", got, skips)
	}
	reasons := map[string]string{}
	for _, s := range skips {
		reasons[s.ID] = s.Reason
	}
	if !strings.Contains(reasons["wf_chain-root"], "resume ancestor of active run") ||
		reasons["wf_paused"] != "paused" || reasons["wf_missing"] != "no such live run" {
		t.Fatalf("explicit skip reasons = %#v", reasons)
	}

	if _, _, err := PlanArchive(ArchiveSelector{Now: now}); err == nil {
		t.Fatal("empty selector should be rejected")
	}
	if _, _, err := PlanArchive(ArchiveSelector{Statuses: []string{"running"}, Now: now}); err == nil {
		t.Fatal("--status running should be rejected")
	}
}

func TestArchivedResumeAncestorStillReadableForConfinedChain(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(ArchiveDirEnv, "")
	writeMetaRun(t, "wf_anc-root", "ok", time.Now().Add(-72*time.Hour), "")
	writeMetaRun(t, "wf_anc-child", "ok", time.Now().Add(-48*time.Hour), "wf_anc-root")
	for _, id := range []string{"wf_anc-root", "wf_anc-child"} {
		if _, err := ArchiveRun(id); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := ResumedFrom("wf_anc-child"); err != nil || got != "wf_anc-root" {
		t.Fatalf("ResumedFrom through archive = %q,%v", got, err)
	}
	f, canon, err := OpenConfinedRunFile("wf_anc-child", "plan.json")
	if err != nil || canon != "wf_anc-child" {
		t.Fatalf("confined plan read = %q, %v", canon, err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if !strings.Contains(string(b), "wf_anc-root") {
		t.Fatalf("plan = %s", b)
	}
}

// A failed ancestry query must never make a live run's parent archivable.
func TestPlanArchiveFailsWhenAncestryStoreCannotBeRead(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(ArchiveDirEnv, "")
	now := time.Now()
	writeMetaRun(t, "wf_protected-parent", "ok", now.Add(-time.Hour), "")
	writeMetaRun(t, "wf_active-child", "running", now, "wf_protected-parent")
	dataDir := store.DataDir()
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE plan_ancestry"); err != nil {
		t.Fatal(err)
	}
	selected, _, err := PlanArchive(ArchiveSelector{AllFinished: true, Now: now})
	if err == nil {
		t.Fatalf("archive ignored ancestry read failure and selected %#v", selected)
	}
	if len(selected) != 0 {
		t.Fatalf("failed archive selected %#v", selected)
	}
	if _, err := os.Stat(filepath.Join(RunsDir(), "wf_protected-parent", "meta.json")); err != nil {
		t.Fatal("ancestor was removed", err)
	}
}

func TestPlanArchiveUsesLineageImportedAtStartup(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(ArchiveDirEnv, t.TempDir())
	now := time.Now()
	writeMetaRun(t, "wf_legacy_parent", "ok", now.Add(-time.Hour), "")
	writeMetaRun(t, "wf_legacy_child", "running", now, "")
	childDir := filepath.Join(RunsDir(), "wf_legacy_child")
	if err := os.WriteFile(filepath.Join(childDir, "plan.json"), []byte("{\"version\":1,\"resumedFrom\":\"wf_legacy_parent\"}"), 0600); err != nil {
		t.Fatal(err)
	}
	dataDir := store.DataDir()
	archived := filepath.Join(ArchiveDir(), "wf_external", "workflows", "nested")
	if err := os.MkdirAll(archived, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archived, "plan.json"), []byte(`{"resumedFrom":"wf_external_parent"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow("SELECT count(*) FROM plan_ancestry").Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("startup imported %d rows: %v", rows, err)
	}
	var externalParent string
	if err := db.QueryRow("SELECT resumed_from FROM plan_ancestry WHERE plan='wf_external/workflows/nested'").Scan(&externalParent); err != nil || externalParent != "wf_external_parent" {
		t.Fatalf("custom archive startup row = %q, %v", externalParent, err)
	}
	selected, skips, err := PlanArchive(ArchiveSelector{AllFinished: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 0 {
		t.Fatalf("needed ancestor became archivable: %#v", selected)
	}
	protected := false
	for _, skip := range skips {
		if skip.ID == "wf_legacy_parent" {
			protected = true
		}
	}
	if !protected {
		t.Fatal("legacy ancestor is not protected")
	}
}
