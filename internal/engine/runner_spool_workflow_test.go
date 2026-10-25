package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/attempts"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func workflowSpoolRun(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	meta := runstore.Meta{ID: id, Status: status, StartedAt: time.Now()}
	if status != "running" {
		meta.EndedAt = time.Now()
	}
	if _, err := db.Exec("INSERT INTO runs(id,session,started,body) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", id, "", meta.StartedAt.UnixNano(), string(arg(t, meta))); err != nil {
		t.Fatal(err)
	}
}

func workflowSpoolRaw(t *testing.T, s *runnerSpool, owner string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, owner+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func workflowSpoolRejected(t *testing.T, s *runnerSpool, owner string, original []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, owner+".rejected.jsonl"))
	if err != nil || !bytes.Equal(raw, original) {
		t.Fatalf("recovery did not retain exact original lines: got %q, want %q: %v", raw, original, err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, owner+".jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drained source remains: %v", err)
	}
}

func workflowSpoolNoReceipt(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, done, err := state.For(db).RequestDone(t.Context(), runnerRequestKey(id)); err != nil || done {
		t.Errorf("recovery invented receipt %s: done=%v, error=%v", id, done, err)
	}
}

func TestWorkflowSpoolAdmissionReplaysTerminalReceipt(t *testing.T) {
	f := newAdmissionFixture(t)
	const owner = "wf_spool_admission"
	workflowSpoolRun(t, f.db, owner, "running")
	in := f.input
	in.ExecutionRef = callspec.ExecutionRef{Run: owner, Agent: 2}
	f.pose(websvc.Caller{Kind: websvc.Worker, Run: owner})
	id := owner + "/native/1"
	ref, err := f.call(id, in)
	if err != nil {
		t.Fatal(err)
	}
	before, err := attempts.Load(t.Context(), f.dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	receipt, done, err := state.For(f.db).RequestDone(t.Context(), runnerRequestKey(id))
	if err != nil || !done {
		t.Fatalf("live admission did not commit its receipt: %v %v", done, err)
	}
	workflowSpoolRun(t, f.db, owner, "ok")
	s := admissionSpool(t, f)
	var logs []string
	s.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	line := engineclient.SpoolLine{ID: id, Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, in)}, At: time.Now()}
	for range 2 {
		writeSpoolFor(t, s, owner, line)
		if err := s.drain(t.Context(), owner); err != nil {
			t.Fatal(err)
		}
	}
	if len(logs) != 0 {
		t.Errorf("own committed admission was rejected: %v", logs)
	}
	if _, err := os.Stat(filepath.Join(s.dir, owner+".rejected.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("own committed admission has a rejection file: %v", err)
	}
	after, err := attempts.Load(t.Context(), f.dir, ref)
	if err != nil || after != before {
		t.Errorf("replay changed the immutable admission: %+v %v", after, err)
	}
	var count int
	if err := f.db.QueryRow("SELECT count(*) FROM account_attempts").Scan(&count); err != nil || count != 1 {
		t.Errorf("replay left %d admission facts: %v", count, err)
	}
	afterReceipt, done, err := state.For(f.db).RequestDone(t.Context(), runnerRequestKey(id))
	if err != nil || !done || !bytes.Equal(receipt, afterReceipt) {
		t.Errorf("replay changed the original acknowledgement: %q %v %v", afterReceipt, done, err)
	}
	for _, kind := range []string{"changed-account", "foreign-execution"} {
		t.Run(kind, func(t *testing.T) {
			changed := in
			if kind == "changed-account" {
				changed.AccountID = "changed"
			} else {
				changed.Run = "wf_other"
			}
			line.Args = []json.RawMessage{arg(t, changed)}
			writeSpoolFor(t, s, owner, line)
			original := workflowSpoolRaw(t, s, owner)
			logs = nil
			if err := s.drain(t.Context(), owner); err != nil {
				t.Fatal(err)
			}
			if len(logs) != 1 || !strings.HasPrefix(logs[0], "bug:") {
				t.Errorf("receipt conflict lost its defect log: %v", logs)
			}
			if kind == "changed-account" && (len(logs) != 1 || !strings.Contains(logs[0], "request ID was reused")) {
				t.Errorf("changed arguments bypassed the fingerprint: %v", logs)
			}
			workflowSpoolRejected(t, s, owner, original)
			if err := os.Remove(filepath.Join(s.dir, owner+".rejected.jsonl")); err != nil {
				t.Fatal(err)
			}
			fact, err := attempts.Load(t.Context(), f.dir, ref)
			if err != nil || fact != before {
				t.Errorf("conflict changed admission: %+v %v", fact, err)
			}
		})
	}
}

func TestWorkflowSpoolUncommittedReceiptsRetainOriginals(t *testing.T) {
	t.Run("admission", func(t *testing.T) {
		f := newAdmissionFixture(t)
		const owner = "wf_spool_uncommitted"
		workflowSpoolRun(t, f.db, owner, "ok")
		s := admissionSpool(t, f)
		var logs []string
		s.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
		in := f.input
		in.ExecutionRef = callspec.ExecutionRef{Run: owner, Agent: 2}
		id := owner + "/native/uncommitted"
		writeSpoolFor(t, s, owner, engineclient.SpoolLine{ID: id, Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, in)}, At: time.Now()})
		original := workflowSpoolRaw(t, s, owner)
		if err := s.drain(t.Context(), owner); err != nil {
			t.Fatal(err)
		}
		workflowSpoolRejected(t, s, owner, original)
		if len(logs) != 1 || !strings.HasPrefix(logs[0], "runner spool of ") || !strings.Contains(logs[0], "without a committed receipt") {
			t.Errorf("uncommitted admission did not get a plain refusal: %v", logs)
		}
		workflowSpoolNoReceipt(t, f.db, id)
		var count int
		if err := f.db.QueryRow("SELECT count(*) FROM account_attempts").Scan(&count); err != nil || count != 0 {
			t.Errorf("offline admission created %d facts: %v", count, err)
		}
	})
}

func TestWorkflowSpoolNeverExecutesSessionMethods(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	m := f.s.rooms.Notify.Sessions
	const owner = "wf_spool_methods"
	workflowSpoolRun(t, f.db, owner, "running")
	before, err := m.Store.ReadMeta(f.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The run table must refuse these calls before any session method runs.
	s, err = newRunnerSpool(f.dir, func() *subagent.Manager {
		t.Error("run recovery entered a session method")
		return m
	}, state.For(f.db), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	lines := []engineclient.SpoolLine{
		{ID: owner + "/pid", Method: "RunnerSetPid", Args: []json.RawMessage{arg(t, 123)}, At: time.Now()},
		{ID: owner + "/finish", Method: "RunnerFinish", Args: []json.RawMessage{arg(t, subagent.RunnerFinish{Turn: 1})}, At: time.Now()},
		{ID: owner + "/end", Method: "RunnerTurnEnd", Args: []json.RawMessage{arg(t, subagent.TurnEnd{Turn: 1, Status: subagent.TurnStopped, Ended: time.Now()})}, At: time.Now()},
	}
	writeSpoolFor(t, s, owner, lines...)
	original := workflowSpoolRaw(t, s, owner)
	if err := s.drain(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	after, err := m.Store.ReadMeta(f.meta.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Errorf("run spool changed session metadata: %+v %v", after, err)
	}
	for _, line := range lines {
		workflowSpoolNoReceipt(t, f.db, line.ID)
	}
	workflowSpoolRejected(t, s, owner, original)
}

func TestWorkflowSpoolRefusesUnsafeOrUnstoredOwnersBeforeMovingFiles(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	for _, owner := range []string{"", ".", "..", "../wf_escape", "wf_nested/child", "wf_back\\slash", "wf_bad\x00", "wf_foreign", "wf_file_only"} {
		t.Run(fmt.Sprintf("%q", owner), func(t *testing.T) {
			if owner == "wf_foreign" {
				if _, err := f.db.Exec("INSERT INTO runs(id,session,started,body) VALUES(?,?,0,?)", owner, "", string(arg(t, runstore.Meta{ID: "wf_other", Status: "ok"}))); err != nil {
					t.Fatal(err)
				}
			}
			if owner == "wf_file_only" {
				path := filepath.Join(f.dir, "runs", owner, "meta.json")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, arg(t, runstore.Meta{ID: owner, Status: "ok"}), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// NUL cannot name a file. Every other owner gets an exact source and
			// leftover drain, including a traversal source outside the spool folder.
			var paths []string
			if !strings.ContainsRune(owner, 0) {
				paths = []string{filepath.Join(s.dir, owner+".jsonl"), filepath.Join(s.dir, owner+".1.draining")}
				for _, path := range paths {
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("invalid owner must leave this untouched\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := s.drain(t.Context(), owner); err == nil {
				t.Errorf("invalid or noncanonical owner %q was accepted", owner)
			}
			for _, path := range paths {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "invalid owner must leave this untouched\n" {
					t.Errorf("owner refusal moved or changed %s: %q %v", path, raw, err)
				}
			}
			var count int
			if err := f.db.QueryRow("SELECT count(*) FROM runs WHERE id='wf_file_only'").Scan(&count); err != nil || count != 0 {
				t.Errorf("drain imported a file-only owner: %d %v", count, err)
			}
		})
	}
}

// rejectedArtifacts lists the immutable files a deleted run's spool became.
func rejectedArtifacts(t *testing.T, s *runnerSpool, owner string) []string {
	t.Helper()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, owner+".") && strings.HasSuffix(name, ".rejected.jsonl") {
			files = append(files, filepath.Join(s.dir, name))
		}
	}
	return files
}

func TestWorkflowSpoolDeletedRunIsRejected(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	var logLines []string
	s.logf = func(format string, args ...any) { logLines = append(logLines, fmt.Sprintf(format, args...)) }
	logs := &logLines
	// The run registered a row, spooled a line, and its row was then deleted,
	// so no drain can ever name its owner again.
	const owner = "wf_spool_deleted"
	workflowSpoolRun(t, f.db, owner, "running")
	line := engineclient.SpoolLine{ID: owner + "/admission/gone", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, owner, line)
	original := workflowSpoolRaw(t, s, owner)
	if _, err := f.db.Exec("DELETE FROM runs WHERE id=?", owner); err != nil {
		t.Fatal(err)
	}
	if err := s.drain(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	retired := rejectedArtifacts(t, s, owner)
	if len(retired) != 1 {
		t.Fatalf("rejected artifacts = %v", retired)
	}
	if got, err := os.ReadFile(retired[0]); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("retired bytes changed: got %q, want %q: %v", got, original, err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, owner+".jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drained source remains: %v", err)
	}
	workflowSpoolNoReceipt(t, f.db, line.ID)
	if len(*logs) != 1 || !strings.Contains((*logs)[0], "its run was deleted") {
		t.Errorf("deleted run spool did not get one rejection line: %v", *logs)
	}
	// A later engine start finds nothing to refuse again, and a retry cannot
	// duplicate the artifact.
	if err := s.drain(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if len(*logs) != 1 || len(rejectedArtifacts(t, s, owner)) != 1 {
		t.Errorf("the rejection repeated: %v", *logs)
	}
}

// A retirement keeps the frozen file exactly: no trimming, no added framing.
func TestWorkflowSpoolDeletedRunRetiresExactBytes(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	var logLines []string
	s.logf = func(format string, args ...any) { logLines = append(logLines, fmt.Sprintf(format, args...)) }
	logs := &logLines
	const owner = "wf_spool_deleted_bytes"
	workflowSpoolRun(t, f.db, owner, "running")
	line := engineclient.SpoolLine{ID: owner + "/admission/gone", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	// Bytes that trimming or line framing would change: a leading space, CRLF
	// and an unterminated tail.
	source := append([]byte(" "), raw...)
	source = append(source, []byte("\r\n\r\n")...)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, owner+".jsonl"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("DELETE FROM runs WHERE id=?", owner); err != nil {
		t.Fatal(err)
	}
	if err := s.drain(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	retired := rejectedArtifacts(t, s, owner)
	if len(retired) != 1 {
		t.Fatalf("rejected artifacts = %v", retired)
	}
	if got, err := os.ReadFile(retired[0]); err != nil || !bytes.Equal(got, source) {
		t.Fatalf("retired bytes changed: got %q, want %q: %v", got, source, err)
	}
	workflowSpoolNoReceipt(t, f.db, line.ID)
	if len(*logs) != 1 {
		t.Errorf("retirement log = %v", *logs)
	}
	if err := s.drain(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if len(rejectedArtifacts(t, s, owner)) != 1 {
		t.Fatal("a retry retired the same file twice")
	}
}

// A run whose row is gone but whose retained files remain is not a deleted
// run: its spool stays refused, byte for byte.
func TestWorkflowSpoolFileOnlyOwnerKeepsItsRefusal(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	const owner = "wf_spool_file_only"
	line := engineclient.SpoolLine{ID: owner + "/admission/file", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, owner, line)
	original := workflowSpoolRaw(t, s, owner)
	dir := filepath.Join(s.runs, owner)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), arg(t, runstore.Meta{ID: owner, Status: "ok"}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.drain(t.Context(), owner); err == nil {
		t.Fatal("a run with retained files was quarantined")
	}
	if got := workflowSpoolRaw(t, s, owner); !bytes.Equal(got, original) {
		t.Fatalf("refusal moved the spool: %q", got)
	}
	if got := rejectedArtifacts(t, s, owner); len(got) != 0 {
		t.Fatalf("refusal quarantined the spool: %v", got)
	}
}

// A retained-entry lookup that fails for a reason other than absence does not
// prove the run was deleted, so the spool keeps its retry path.
func TestWorkflowSpoolLookupErrorKeepsItsRetryPath(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	const owner = "wf_spool_lookup"
	line := engineclient.SpoolLine{ID: owner + "/admission/lookup", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, owner, line)
	source := workflowSpoolRaw(t, s, owner)
	// The lookup fails with ENOTDIR, which is not proof of absence.
	if err := os.MkdirAll(filepath.Dir(s.runs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.runs, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.drain(t.Context(), owner); err == nil {
		t.Fatal("a lookup error was treated as a deleted run")
	}
	if got := workflowSpoolRaw(t, s, owner); !bytes.Equal(got, source) {
		t.Fatalf("the refusal moved the spool: %q", got)
	}
	if artifacts := rejectedArtifacts(t, s, owner); len(artifacts) != 0 {
		t.Fatalf("the refusal retired the spool: %v", artifacts)
	}
	workflowSpoolNoReceipt(t, f.db, line.ID)
	// A retained symlink counts as retained even when its target is gone.
	if err := os.Remove(s.runs); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.runs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), filepath.Join(s.runs, owner)); err != nil {
		t.Fatal(err)
	}
	if err := s.drain(t.Context(), owner); err == nil {
		t.Fatal("a retained symlink was treated as a deleted run")
	}
	if got := workflowSpoolRaw(t, s, owner); !bytes.Equal(got, source) {
		t.Fatalf("the symlink refusal moved the spool: %q", got)
	}
}

// A frozen name that an earlier retirement already used must never lose its
// bytes: the new retirement takes another name.
func TestWorkflowSpoolRetirementNeverReplacesAnArtifact(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	const owner = "wf_spool_collision"
	// A fixed clock makes the taken file name, and so the first artifact name,
	// repeatable.
	fixed := time.Unix(1_700_000_000, 123)
	s.now = func() time.Time { return fixed }
	line := engineclient.SpoolLine{ID: owner + "/admission/collision", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, owner, line)
	source := workflowSpoolRaw(t, s, owner)
	earlier := filepath.Join(s.dir, fmt.Sprintf("%s.%d.draining.rejected.jsonl", owner, fixed.UnixNano()))
	if err := os.WriteFile(earlier, []byte("earlier artifact\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.drain(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(earlier); err != nil || string(got) != "earlier artifact\n" {
		t.Fatalf("an earlier artifact was replaced: %q %v", got, err)
	}
	files := rejectedArtifacts(t, s, owner)
	kept := false
	for _, file := range files {
		if file == earlier {
			continue
		}
		if b, err := os.ReadFile(file); err == nil && bytes.Equal(b, source) {
			kept = true
		}
	}
	if len(files) != 2 || !kept {
		t.Fatalf("both sources were not kept byte for byte: %v", files)
	}
}

// A frozen file that a stopped drain left keeps its bytes: a new take
// reserves another name instead of replacing it.
func TestWorkflowSpoolTakeNeverReplacesAFrozenFile(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	const owner = "wf_spool_take"
	// A fixed clock makes the name a take would choose repeatable.
	fixed := time.Unix(1_700_000_000, 456)
	s.now = func() time.Time { return fixed }
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	frozen := filepath.Join(s.dir, fmt.Sprintf("%s.%d.draining", owner, fixed.UnixNano()))
	frozenBytes := []byte("frozen drain bytes\n")
	if err := os.WriteFile(frozen, frozenBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	line := engineclient.SpoolLine{ID: owner + "/quota/take", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, owner, line)
	active := workflowSpoolRaw(t, s, owner)
	if err := s.drain(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	files := rejectedArtifacts(t, s, owner)
	if len(files) != 2 {
		t.Fatalf("artifacts = %v", files)
	}
	var haveFrozen, haveActive bool
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(b, frozenBytes) {
			haveFrozen = true
		}
		if bytes.Equal(b, active) {
			haveActive = true
		}
	}
	if !haveFrozen || !haveActive {
		t.Fatalf("both sources were not kept byte for byte: %v", files)
	}
	workflowSpoolNoReceipt(t, f.db, line.ID)
}

// A rename is durable only after its directory entries are synced. The sync
// must follow the rename, and a failed sync must keep the recoverable file so
// a retry can finish the work.
func TestWorkflowSpoolRetirementSyncsBeforeSuccess(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	m := f.s.rooms.Notify.Sessions
	const owner = "wf_spool_durable"
	line := engineclient.SpoolLine{ID: owner + "/quota/durable", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, owner, line)
	source := workflowSpoolRaw(t, s, owner)

	saved := syncDir
	t.Cleanup(func() { syncDir = saved })
	type step struct {
		sourceGone bool
		artifacts  int
	}
	var steps []step
	syncDir = func(string) error {
		_, srcErr := os.Stat(filepath.Join(s.dir, owner+".jsonl"))
		artifacts, _ := filepath.Glob(filepath.Join(s.dir, owner+".*.rejected.jsonl"))
		steps = append(steps, step{errors.Is(srcErr, os.ErrNotExist), len(artifacts)})
		return nil
	}
	if err := s.drain(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("directory syncs = %d, want one after the take and one after the retirement", len(steps))
	}
	if !steps[0].sourceGone || steps[0].artifacts != 0 {
		t.Fatalf("the take synced before its rename: %+v", steps[0])
	}
	if steps[1].artifacts != 1 {
		t.Fatalf("the retirement synced before its rename: %+v", steps[1])
	}
	retired := rejectedArtifacts(t, s, owner)
	if len(retired) != 1 {
		t.Fatalf("artifacts = %v", retired)
	}
	if got, err := os.ReadFile(retired[0]); err != nil || !bytes.Equal(got, source) {
		t.Fatalf("retired bytes changed: %q %v", got, err)
	}

	// A failed sync keeps the recoverable file, and a new spool over the same
	// directory finishes the retirement exactly once.
	const retryOwner = "wf_spool_durable_retry"
	retryLine := engineclient.SpoolLine{ID: retryOwner + "/quota/retry", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, retryOwner, retryLine)
	retrySource := workflowSpoolRaw(t, s, retryOwner)
	fails := 1
	syncDir = func(string) error {
		if fails > 0 {
			fails--
			return errors.New("sync failed")
		}
		return nil
	}
	if err := s.drain(t.Context(), retryOwner); err == nil {
		t.Fatal("a failed directory sync was reported as success")
	}
	frozen, err := filepath.Glob(filepath.Join(s.dir, retryOwner+".*.draining"))
	if err != nil || len(frozen) != 1 {
		t.Fatalf("frozen file after the failed sync = %v %v", frozen, err)
	}
	again, err := newRunnerSpool(f.dir, func() *subagent.Manager { return m }, state.For(f.db), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.drain(t.Context(), retryOwner); err != nil {
		t.Fatal(err)
	}
	retried := rejectedArtifacts(t, again, retryOwner)
	if len(retried) != 1 {
		t.Fatalf("retry artifacts = %v", retried)
	}
	if got, err := os.ReadFile(retried[0]); err != nil || !bytes.Equal(got, retrySource) {
		t.Fatalf("retried bytes changed: %q %v", got, err)
	}
	workflowSpoolNoReceipt(t, f.db, retryLine.ID)

	// A retirement whose own rename succeeded but whose sync failed has no
	// source left. The retry must sync the directory again before it reports
	// success, or the artifact stays unconfirmed.
	const lateOwner = "wf_spool_durable_late"
	lateLine := engineclient.SpoolLine{ID: lateOwner + "/quota/late", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, lateOwner, lateLine)
	lateSource := workflowSpoolRaw(t, s, lateOwner)
	calls := 0
	syncDir = func(string) error {
		calls++
		if calls == 2 {
			return errors.New("sync failed")
		}
		return nil
	}
	if err := s.drain(t.Context(), lateOwner); err == nil {
		t.Fatal("a failed retirement sync was reported as success")
	}
	if got := rejectedArtifacts(t, s, lateOwner); len(got) != 1 {
		t.Fatalf("artifact after the failed retirement sync = %v", got)
	}
	if err := s.drain(t.Context(), lateOwner); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("the retry returned without syncing again: %d syncs", calls)
	}
	if got, err := os.ReadFile(rejectedArtifacts(t, s, lateOwner)[0]); err != nil || !bytes.Equal(got, lateSource) {
		t.Fatalf("retried bytes changed: %q %v", got, err)
	}
	workflowSpoolNoReceipt(t, f.db, lateLine.ID)

	// A run id may hold characters that a glob pattern would read as pattern
	// syntax. The retry must still confirm the directory, so it must not depend
	// on an owner-derived lookup.
	const metaOwner = "wf_["
	mtLine := engineclient.SpoolLine{ID: metaOwner + "/quota/meta", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, metaOwner, mtLine)
	mtSource := workflowSpoolRaw(t, s, metaOwner)
	metaCalls := 0
	syncDir = func(string) error {
		metaCalls++
		if metaCalls == 2 {
			return errors.New("sync failed")
		}
		return nil
	}
	if err := s.drain(t.Context(), metaOwner); err == nil {
		t.Fatal("a failed retirement sync was reported as success")
	}
	if err := s.drain(t.Context(), metaOwner); err != nil {
		t.Fatal(err)
	}
	if metaCalls != 3 {
		t.Fatalf("a pattern-like run id skipped the retry sync: %d syncs", metaCalls)
	}
	mtArtifacts := rejectedArtifacts(t, s, metaOwner)
	if len(mtArtifacts) != 1 {
		t.Fatalf("pattern-like id artifacts = %v", mtArtifacts)
	}
	if got, err := os.ReadFile(mtArtifacts[0]); err != nil || !bytes.Equal(got, mtSource) {
		t.Fatalf("pattern-like id changed the bytes: %q %v", got, err)
	}
	workflowSpoolNoReceipt(t, f.db, mtLine.ID)
}

func TestWorkflowSpoolClaimedSessionActorCannotDrainRun(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	const owner = "wf_claimed"
	line := engineclient.SpoolLine{ID: owner + "/claimed", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, owner, line)
	before := workflowSpoolRaw(t, s, owner)
	calls := map[string]websvc.Call{"AdmitNativeAttempt": func(context.Context, []json.RawMessage) (any, error) { return nil, nil }}
	s.guard(calls)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: owner})
	if _, err := calls["AdmitNativeAttempt"](ctx, nil); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(s.dir, owner+".jsonl")); err != nil || !bytes.Equal(raw, before) {
		t.Fatalf("an unverified actor claim drained the run spool: %s %v", raw, err)
	}
}

func TestWorkflowSpoolTreatsOwnerAsLiteralPathElement(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	const owner = "wf_literal[1].part"
	const foreign = "wf_literal1.part"
	workflowSpoolRun(t, f.db, owner, "ok")
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		t.Fatal(err)
	}
	line := func(id string) []byte {
		return append(arg(t, engineclient.SpoolLine{ID: id + "/uncommitted", Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}), '\n')
	}
	ownPath := filepath.Join(s.dir, owner+".100.draining")
	foreignPath := filepath.Join(s.dir, foreign+".200.draining")
	own, other := line(owner), line(foreign)
	if err := os.WriteFile(ownPath, own, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignPath, other, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.drain(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ownPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("literal owner's taken file was missed: %v", err)
	}
	if raw, err := os.ReadFile(foreignPath); err != nil || !bytes.Equal(raw, other) {
		t.Fatalf("literal owner consumed another run's file: %s %v", raw, err)
	}
	workflowSpoolRejected(t, s, owner, own)
}
