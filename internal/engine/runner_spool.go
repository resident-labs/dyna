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
	"slices"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// runnerSpool applies the writes a turn runner spooled while it could not
// reach the engine (engineclient.SpoolLine). Each line goes through the same
// runner call table, as the session the file belongs to, under the request
// ID the runner sent it with, so a write the engine applied before is not
// applied twice. A line that is not a runner call, that is older than the
// engine remembers request IDs, or that the call refuses moves to
// <session>.rejected.jsonl. An uncommitted receipt-only call gets a plain log;
// other rejections are logged as bugs. Only a transient error
// stops a drain; the file then stays for the next one.
type runnerSpool struct {
	dir          string
	runs         string
	calls        map[string]websvc.Call
	receiptCalls map[string]websvc.Call
	once         *requestRunner
	w            *state.Writer
	now          func() time.Time
	logf         func(string, ...any)
	mu           sync.Mutex
	// afterRead, when set, runs after a taken spool file is read. Tests
	// use it to append a line at that moment.
	afterRead func()
}

var errUncommittedSpoolReceipt = errors.New("offline recovery cannot apply a call without a committed receipt")

// errSpoolOwnerGone marks a spool whose run row no longer exists: the run was
// deleted before its spool drained. Such a spool is rejected, never applied.
var errSpoolOwnerGone = errors.New("the run that owns this spool was deleted")

// syncDir flushes the spool directory after a rename, so the frozen or retired
// name survives a crash. Tests replace it to observe the ordering.
var syncDir = fsutil.SyncDir

func newRunnerSpool(dataDir string, sessions func() *subagent.Manager, w *state.Writer, logf func(string, ...any)) (*runnerSpool, error) {
	// The spool's own copy of the runner table is engine-internal and never
	// served, so it drops the socket-only check: a drain runs at engine start
	// and at a runner's exit, with no socket caller.
	var bindings []websvc.Binding
	for _, binding := range RunnerBindings(sessions) {
		if binding.Access == websvc.Write {
			bindings = append(bindings, binding)
		}
	}
	// Admission and best-effort quota calls may remain ahead of terminal
	// writes. Recovery may replay committed receipts through once, but must
	// not apply either call without its verified live caller.
	receiptBindings := []websvc.Binding{{
		Name: "AdmitNativeAttempt", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(callspec.AccountAdmission) error {
			return errUncommittedSpoolReceipt
		},
	}, {
		Name: "ObserveMuseQuota", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(callspec.MuseQuotaObservation) error {
			return errUncommittedSpoolReceipt
		},
	}, {
		Name: "ObserveClaudeUsage", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(callspec.ClaudeUsageObservation) error {
			return errUncommittedSpoolReceipt
		},
	}}
	bindings = append(bindings, receiptBindings...)
	bindings = append(bindings, runDirectoriesRecoveryBinding(w), runFinishRecoveryBinding(w))
	for i := range bindings {
		bindings[i].Local = false
	}
	calls, err := websvc.Bind(bindings)
	if err != nil {
		return nil, err
	}
	receiptCalls := map[string]websvc.Call{}
	for _, binding := range receiptBindings {
		receiptCalls[binding.Name] = calls[binding.Name]
	}
	receiptCalls["AddRunDirectories"] = calls["AddRunDirectories"]
	receiptCalls["FinishRun"] = calls["FinishRun"]
	return &runnerSpool{receiptCalls: receiptCalls, dir: sessionlife.RunnerSpoolDir(dataDir), runs: filepath.Join(dataDir, "runs"), calls: calls, once: &requestRunner{w: w, logf: logf}, w: w, now: time.Now, logf: logf}, nil
}

// spoolMargin keeps a line well inside request-ID retention: the engine
// prunes request IDs hourly, so a line within an hour of
// state.RequestKeep could name an ID that is gone.
const spoolMargin = time.Hour

// drain applies session id's spool. It first renames the spool file to a
// unique .draining name, so a line the runner appends meanwhile goes to a
// new spool file, which the next drain applies: no line is removed unread.
// A .draining file that a stopped drain left is applied first, in order.
func (s *runnerSpool) drain(ctx context.Context, id string) error {
	actor, calls, err := s.owner(ctx, id)
	if errors.Is(err, errSpoolOwnerGone) {
		return s.reject(ctx, id)
	}
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, id+".jsonl")
	left, _ := s.drainingFiles(id)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && len(left) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.drainingFiles(id)
	if err != nil {
		return err
	}
	if taken, err := s.take(ctx, id, path); err != nil {
		return err
	} else if taken != "" {
		files = append(files, taken)
	}
	slices.Sort(files) // the names carry the time they were taken
	for _, file := range files {
		if err := s.apply(ctx, id, file, actor, calls); err != nil {
			return err
		}
	}
	return nil
}

// take renames session id's spool file to a unique .draining name, under
// the spool lock the runner holds around its appends, and returns that name,
// or "" when there is no spool file. The frozen name is reserved first, so a
// take never replaces a frozen file that a stopped drain left.
func (s *runnerSpool) take(ctx context.Context, id, path string) (string, error) {
	unlock, err := sessionlife.LockRunnerSpool(ctx, s.dir, id)
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	draining, err := s.claimDraining(id)
	if err != nil {
		return "", err
	}
	if err := os.Rename(path, draining); err != nil {
		// The reservation is empty, so a failed rename leaves no frozen file.
		_ = os.Remove(draining)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	// The frozen name is durable once the directory entries are synced.
	if err := syncDir(s.dir); err != nil {
		return "", err
	}
	return draining, nil
}

// claimDraining reserves a frozen name that no earlier take used. The reserved
// file is empty until the rename fills it.
func (s *runnerSpool) claimDraining(id string) (string, error) {
	stamp := s.now().UnixNano()
	return reserve(func(i int64) string {
		return filepath.Join(s.dir, fmt.Sprintf("%s.%d.draining", id, stamp+i))
	})
}

// apply applies one taken spool file of session id and removes it. A
// transient error stops it and keeps the file; the lines it rejected so far
// are then rejected and logged again by the next drain.
func (s *runnerSpool) apply(ctx context.Context, id, file string, owner authority.Actor, calls map[string]websvc.Call) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if s.afterRead != nil {
		s.afterRead()
	}
	actor := authority.WithActor(ctx, owner)
	var rejected [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var l engineclient.SpoolLine
		reason := ""
		expected := false
		decodeErr := json.Unmarshal(line, &l)
		call, ok := calls[l.Method]
		switch {
		case decodeErr != nil:
			reason = "not a spool line"
		case !ok:
			reason = "not a runner call"
		case l.ID == "" || callspec.CheckRunnerRequestID(id, l.ID) != nil:
			reason = "its request ID names another session"
		case s.now().Sub(l.At) >= state.RequestKeep-spoolMargin:
			reason = "older than the engine remembers request IDs"
		default:
			// The key and request ID the live call used.
			if _, err := s.once.run(callspec.WithRequest(actor, l.ID), runnerRequestKey(l.ID), l.Method, call, l.Args); err != nil {
				if websvc.Transient(err) {
					return err
				}
				reason = err.Error()
				expected = errors.Is(err, errUncommittedSpoolReceipt)
			}
		}
		if reason != "" {
			if expected {
				s.logf("runner spool of %s: %s %s rejected: %s", id, l.Method, l.ID, reason)
			} else {
				s.logf("bug: runner spool of %s: %s %s rejected: %s", id, l.Method, l.ID, reason)
			}
			rejected = append(rejected, line)
		}
	}
	for _, line := range rejected {
		if err := s.rejectLine(ctx, id, line); err != nil {
			return err
		}
	}
	return os.Remove(file)
}

// rejectLine appends one line a spool cannot apply to its rejected file, so
// the original bytes stay for inspection.
func (s *runnerSpool) rejectLine(ctx context.Context, id string, line []byte) error {
	return s.w.AppendSynced(ctx, hub.Event{}, filepath.Join(s.dir, id+".rejected.jsonl"), line)
}

// reject retires the spool of a run whose canonical row is gone. Each taken
// file keeps its exact bytes as an immutable <file>.rejected.jsonl artifact,
// and no call is applied, so a deleted run never grants authority or repeats
// work. A rename is atomic, so an interrupted retirement keeps the source for
// the next drain and can never duplicate bytes.
func (s *runnerSpool) reject(ctx context.Context, id string) error {
	path := filepath.Join(s.dir, id+".jsonl")
	s.mu.Lock()
	defer s.mu.Unlock()
	left, err := s.drainingFiles(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && len(left) == 0 {
		// Nothing is left to rename, but an earlier retirement's rename may
		// not be confirmed yet. Sync the directory before reporting success.
		// This needs no owner-derived lookup, so any run id is safe, and a
		// clean directory costs one cheap sync.
		return syncDir(s.dir)
	}
	files, err := s.drainingFiles(id)
	if err != nil {
		return err
	}
	if taken, err := s.take(ctx, id, path); err != nil {
		return err
	} else if taken != "" {
		files = append(files, taken)
	}
	slices.Sort(files)
	for _, file := range files {
		retired, err := s.claimRetired(file)
		if err != nil {
			return err
		}
		if err := os.Rename(file, retired); err != nil {
			// The reservation is empty, so a failed rename leaves no artifact.
			_ = os.Remove(retired)
			return err
		}
		// The rename is durable once the directory entries are synced.
		if err := syncDir(s.dir); err != nil {
			return err
		}
		s.logf("runner spool of %s rejected: its run was deleted", id)
	}
	return nil
}

// claimRetired reserves a frozen name that no earlier retirement used, so a
// rename can never replace an existing artifact. The reserved file is empty
// until the rename fills it.
func (s *runnerSpool) claimRetired(file string) (string, error) {
	base := file + ".rejected.jsonl"
	stem := strings.TrimSuffix(base, ".rejected.jsonl")
	return reserve(func(i int64) string {
		if i == 0 {
			return base
		}
		return fmt.Sprintf("%s.%d.rejected.jsonl", stem, i)
	})
}

// reserve creates an empty file whose name no earlier file used, so a rename
// onto it can never replace bytes. next builds the candidate name for each
// attempt; the reserved file stays empty until that rename fills it.
func reserve(next func(i int64) string) (string, error) {
	for i := int64(0); ; i++ {
		held, err := os.OpenFile(next(i), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return held.Name(), held.Close()
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
}

// drainAll drains every session that has a spool, at engine start: one
// listing of the spool folder, not a walk over session folders.
func (s *runnerSpool) drainAll(ctx context.Context) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var failures error
	seen := map[string]bool{}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		name := e.Name()
		id, ok := strings.CutSuffix(name, ".jsonl")
		if strings.HasSuffix(name, ".draining") {
			id, ok = drainingOwner(name)
		}
		if !ok || strings.HasSuffix(id, ".rejected") || seen[id] {
			continue
		}
		seen[id] = true
		if err := s.drain(ctx, id); err != nil {
			failures = errors.Join(failures, fmt.Errorf("runner spool of %s: %w", id, err))
		}
	}
	return failures
}

// guard drains a session's spool before any new runner call from it is
// applied, so a spooled write always lands before a later live one. It runs
// inside the engine's actor wrapper (actForCallers), so the caller's session
// is its actor's, the same one the runner call itself writes.
func (s *runnerSpool) guard(calls map[string]websvc.Call) {
	for name := range s.calls {
		call, ok := calls[name]
		if !ok {
			continue
		}
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			owner := authority.Of(ctx).Session
			if !runstore.IsSessionDirID(owner) {
				owner = ""
			}
			if caller, local := websvc.CallerFromContext(ctx); local && caller.Kind == websvc.Worker && caller.Agent == 0 && caller.Run != "" && caller.Session == "" {
				owner = caller.Run
			}
			if session := owner; session != "" {
				if err := s.drain(ctx, session); err != nil {
					return nil, err
				}
			}
			return call(ctx, args)
		}
	}
}

// owner validates the file owner before any path or authority is constructed.
// A run's canonical row, including terminal or archived rows, names its owner.
// A run whose row was deleted names none: it returns errSpoolOwnerGone, so the
// spool is rejected instead of waiting forever.
func (s *runnerSpool) owner(ctx context.Context, id string) (authority.Actor, map[string]websvc.Call, error) {
	if runstore.IsSessionDirID(id) {
		return authority.Actor{Role: authority.Session, Session: id}, s.calls, nil
	}
	if err := runstore.ValidateRunID(id); err != nil {
		return authority.Actor{}, nil, err
	}
	db, err := database.Open(filepath.Dir(s.dir))
	if err != nil {
		return authority.Actor{}, nil, err
	}
	meta, err := runstore.LoadStored(ctx, db, id)
	if errors.Is(err, sql.ErrNoRows) {
		// A missing row is a deleted run only when the retained run entry is
		// proven absent. A file-only owner, a retained symlink and a lookup
		// error all keep the spool refused, as they did before this outcome
		// existed.
		_, lookup := os.Lstat(filepath.Join(s.runs, id))
		switch {
		case lookup == nil:
			return authority.Actor{}, nil, fmt.Errorf("workflow spool owner %s has no row but keeps its files: %w", id, err)
		case !errors.Is(lookup, os.ErrNotExist):
			return authority.Actor{}, nil, fmt.Errorf("workflow spool owner %s: %w", id, lookup)
		}
		return authority.Actor{}, nil, errSpoolOwnerGone
	}
	if err != nil {
		return authority.Actor{}, nil, fmt.Errorf("workflow spool owner %s: %w", id, err)
	}
	if meta.ID != id {
		return authority.Actor{}, nil, errors.New("workflow spool owner differs from its canonical row")
	}
	return authority.Actor{Role: authority.Worker, Run: id}, s.receiptCalls, nil
}

func drainingOwner(name string) (string, bool) {
	base, ok := strings.CutSuffix(name, ".draining")
	if !ok {
		return "", false
	}
	last := strings.LastIndexByte(base, '.')
	if last < 1 {
		return "", false
	}
	return base[:last], true
}

// IDs are literal path elements, including dots and glob metacharacters.
func (s *runnerSpool) drainingFiles(id string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, "*.draining"))
	if err != nil {
		return nil, err
	}
	out := files[:0]
	for _, file := range files {
		if owner, ok := drainingOwner(filepath.Base(file)); ok && owner == id {
			out = append(out, file)
		}
	}
	return out, nil
}
