// Package runstore persists workflow runs: metadata, an append-only event
// journal (tailed live by `dyna runs` and the desktop app), a journal of full agent results, and the
// final result.
package runstore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"dyna-agent/internal/callspec"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/procident"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/workeroutput"
	"dyna-agent/internal/workeroutput/projection"
)

// Event is one line in events.jsonl.
type Event struct {
	T              string `json:"t"`  // run_start|workflow_start|phase|agent_start|agent_run|agent_journal|agent_nudge|agent_steer|agent_defer|agent_end|log|workflow_end|run_end
	TS             int64  `json:"ts"` // unix millis
	Title          string `json:"title,omitempty"`
	ID             int    `json:"id,omitempty"`
	Label          string `json:"label,omitempty"`
	Profile        string `json:"profile,omitempty"`
	Phase          string `json:"phase,omitempty"`
	Msg            string `json:"msg,omitempty"`
	Provenance     string `json:"provenance,omitempty"` // workflow|system; t=log only
	Kind           string `json:"kind,omitempty"`
	Status         string `json:"status,omitempty"` // agent_end/run_end status, or sent|unavailable|ignored for agent_nudge
	DurMs          int64  `json:"durMs,omitempty"`
	Preview        string `json:"preview,omitempty"`
	Error          string `json:"error,omitempty"`
	Cached         bool   `json:"cached,omitempty"`   // satisfied from a resumed run's journal
	Dir            string `json:"dir,omitempty"`      // retained worktree path, when isolated
	Worktree       string `json:"worktree,omitempty"` // retained|removed; absent when no worktree was created
	Workflow       string `json:"workflow,omitempty"` // nested workflow id
	Parent         string `json:"parent,omitempty"`   // parent run id for workflow_start
	Ref            string `json:"ref,omitempty"`      // resolved nested workflow script path
	PlanNodeID     string `json:"planNodeId,omitempty"`
	PlanGroupID    string `json:"planGroupId,omitempty"`
	WorkflowPlanID string `json:"workflowPlanId,omitempty"`
	StageIndex     *int   `json:"stageIndex,omitempty"`
	ItemIndex      *int   `json:"itemIndex,omitempty"`
}

// JournalEntry is one line in journal.jsonl: the full record of one agent
// call, also used as the cache source for --resume.
type JournalEntry struct {
	ID       int    `json:"id"`
	Label    string `json:"label"`
	Profile  string `json:"profile"`
	Key      string `json:"key"`           // versioned call identity for resume matching
	Cwd      string `json:"cwd,omitempty"` // effective worker cwd before worktree isolation
	Prompt   string `json:"prompt"`
	Result   any    `json:"result"`
	Error    string `json:"error,omitempty"`
	Cached   bool   `json:"cached,omitempty"`
	Dir      string `json:"dir,omitempty"`
	Worktree string `json:"worktree,omitempty"` // retained|removed; absent when no worktree was created
	Workflow string `json:"workflow,omitempty"`
}

// AgentJournalEnv is injected into a worker process with the absolute path of
// that agent's append-only journal.
const AgentJournalEnv = "DYNA_AGENT_JOURNAL"

// SessionEnv attributes a workflow to the interactive harness session that
// launched it.
const SessionEnv = mode.EnvSession

// OwnerFromEnv names the agent session that is starting dyna work, so runs,
// subagent sessions, and shells can be followed per owner: $DYNA_SESSION when
// set (subagent sessions and integrations set it), else the calling harness's
// own session: "claude:<session id>" inside Claude Code. Empty for a plain
// shell.
func OwnerFromEnv() string {
	clip := func(s string) string {
		s = strings.TrimSpace(s)
		if len(s) > 128 {
			s = s[:128]
		}
		return s
	}
	if s := clip(os.Getenv(SessionEnv)); s != "" {
		return s
	}
	if c := clip(os.Getenv("CLAUDE_CODE_SESSION_ID")); c != "" {
		return clip("claude:" + c)
	}
	return ""
}

// WorkerRunFromEnv names the workflow run whose worker starts a process
// (DYNA_WORKER_RUN). It is empty outside workflow workers, and an id without
// the run id shape is ignored.
func WorkerRunFromEnv() string {
	run := strings.TrimSpace(os.Getenv(mode.EnvWorkerRun))
	if len(run) > 128 {
		run = run[:128]
	}
	if run == "" {
		return ""
	}
	if err := ValidateRunID(run); err != nil {
		return ""
	}
	return run
}

// ModeEnv names the dyna mode a process runs in: "workflow" for a workflow
// worker, "subagent" for a subagent session's worker. Unset for humans.
const ModeEnv = mode.EnvMode

// AgentJournalRootEnv pins journal-path validation to the current run even if
// a worker profile overrides HOME or XDG_DATA_HOME.
const AgentJournalRootEnv = "DYNA_AGENT_JOURNAL_ROOT"

const (
	agentJournalMaxRecordBytes     = 64 * 1024 * 1024
	agentJournalMaxMessageBytes    = 4096
	agentJournalMaxKindBytes       = 32
	normalizedOutputMaxRecordBytes = 4 * 1024 * 1024
	// MaxSteeringMessageBytes keeps steering deliberately short and bounds
	// the prompt injected into an already-running agent session.
	MaxSteeringMessageBytes = 2000
)

// Durable t=log provenance. Only LogProvenanceWorkflow is authored by the
// public script log() binding. Engine diagnostics use LogProvenanceSystem.
// Older events may omit the field; treat anything other than workflow as
// non-workflow so consumers do not match message phrases.
const (
	LogProvenanceWorkflow = "workflow"
	LogProvenanceSystem   = "system"
)

// IsWorkflowLog reports whether this is a durable t=log event from the public
// workflow log() binding.
func (e Event) IsWorkflowLog() bool {
	return e.T == "log" && e.Provenance == LogProvenanceWorkflow
}

// CanonicalLogProvenance maps an emit-time mark to a stable value. Only an
// explicit workflow mark is preserved; everything else is system.
func CanonicalLogProvenance(p string) string {
	if strings.EqualFold(strings.TrimSpace(p), LogProvenanceWorkflow) {
		return LogProvenanceWorkflow
	}
	return LogProvenanceSystem
}

// AgentJournalEntry is one line in agents/<numeric-id>/journal.jsonl. Start
// records carry the system metadata needed to understand the worker in
// isolation; subsequent records carry progress.
type AgentJournalEntry struct {
	TS      int64  `json:"ts"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Next    string `json:"next,omitempty"`
	Source  string `json:"source"`

	AgentID int    `json:"agentId,omitempty"`
	Label   string `json:"label,omitempty"`
	Profile string `json:"profile,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

// SteeringMessage is one accepted instruction for a running agent session.
type SteeringMessage struct {
	TS      int64  `json:"ts"`
	Message string `json:"message"`
	// From and ID name the room post this message frames, when it is one.
	From string `json:"from,omitempty"`
	ID   string `json:"id,omitempty"`
}

// Meta is a run's record. It lives in dyna.db.
type Meta struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"` // running|ok|error|canceled
	Session string `json:"session,omitempty"`
	// ParentSession is the subagent session that started this run (set when
	// the run is launched from inside a subagent session).
	ParentSession string `json:"parentSession,omitempty"`
	RoomID        string `json:"roomId,omitempty"`
	Room          string `json:"room,omitempty"`
	Detached      bool   `json:"detached,omitempty"`
	Pid           int    `json:"pid,omitempty"`
	// PidStart is the process start time (clock ticks after boot) that tells
	// this run's process from a later one the kernel gives the same PID.
	PidStart  uint64    `json:"pidStart,omitempty"`
	Args      any       `json:"args,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
	Error     string    `json:"error,omitempty"`
	// Paused is set while a running run starts no new workers. SetPaused
	// writes it; the end of the run clears it.
	Paused bool `json:"paused,omitempty"`
	// Canceling is set once cancellation has started, before the signal: shell
	// publication refuses new worker shells for the run behind the cancel
	// cleanup's snapshot (D-165).
	Canceling bool `json:"canceling,omitempty"`
}

// Run is an open, writable run directory.
type Run struct {
	Dir  string
	Meta Meta

	mu                sync.Mutex
	recordDirectories func([]string) error
	finishRun         func(callspec.RunFinish) (Meta, error)
	directoryErr      error
	finishing         bool
	// dirs is the path of the list of every worker directory that the run's
	// events and journal name, so worktree owner lookup reads it instead of
	// the large journals. It is "" when the run keeps no list.
	dirs     string
	dirsSeen map[string]bool
	// dirsStale: a list write failed and the list could not be deleted yet.
	dirsStale bool
}

// DirsFile is the per-run list of worker directories, one JSON string per
// line, each written before the event or journal entry that names it.
const DirsFile = "dirs.jsonl"

func RunsDir() string { return filepath.Join(store.DataDir(), "runs") }

// SessionsDir holds subagent sessions next to the runs.
func SessionsDir() string { return filepath.Join(store.DataDir(), "sessions") }

// SessionDirPrefix starts every subagent session id. It differs from the run
// prefix so a session id can never be mistaken for a run id.
const SessionDirPrefix = "ses_"

// IsSessionDirID reports whether id is shaped like a subagent session id.
func IsSessionDirID(id string) bool {
	return strings.HasPrefix(id, SessionDirPrefix) && id != SessionDirPrefix &&
		filepath.Base(id) == id && !strings.ContainsAny(id, `/\`) && !strings.ContainsRune(id, 0)
}

// ReadNormalizedOutputPath parses normalized output records from an absolute
// output.jsonl path (used for subagent sessions, whose files live outside runs).
func ReadNormalizedOutputPath(path string, offset, limit int64) ([]workeroutput.Event, int64, error) {
	return readJSONLinesLimitedFrom[workeroutput.Event](path, offset, limit, normalizedOutputMaxRecordBytes)
}

// ReadNormalizedOutputTailPath reads a range while retaining at most maxEvents
// newest records. The returned offset includes all complete records scanned.
func ReadNormalizedOutputTailPath(path string, offset, limit int64, maxEvents int) ([]workeroutput.Event, int64, error) {
	return readJSONLines[workeroutput.Event](path, offset, limit, normalizedOutputMaxRecordBytes, jsonLinesPolicy{keepLast: maxEvents}, workeroutput.IsCompactionEvent)
}

// ReadProjectedNormalizedOutputPath resolves conversions by stable artifact key.
func ReadProjectedNormalizedOutputPath(dataDir, key, path string, offset, limit int64) ([]workeroutput.Event, int64, error) {
	return ReadProjectedNormalizedOutputTailPath(dataDir, key, path, offset, limit, 0)
}

// ReadProjectedNormalizedOutputTailPath bounds retained events while preserving
// the original source cursor, including malformed complete source lines.
func ReadProjectedNormalizedOutputTailPath(dataDir, key, path string, offset, limit int64, maxEvents int) ([]workeroutput.Event, int64, error) {
	reader, err := projection.Open(dataDir, key, path)
	if err != nil {
		return nil, offset, err
	}
	defer reader.Close()
	return readJSONLinesDecoded(path, offset, limit, normalizedOutputMaxRecordBytes, jsonLinesPolicy{keepLast: maxEvents}, func(start, end int64, raw []byte) (event workeroutput.Event, ok bool, err error) {
		event, ok, err = reader.Event(start, end, raw)
		if err != nil {
			return event, false, err
		}
		if !ok {
			if err := json.Unmarshal(raw, &event); err != nil {
				return event, false, nil
			}
		}
		event.SourceOffset = start
		return event, true, nil
	}, workeroutput.IsCompactionEvent)
}

// NewID mints a run id; exported so a detaching parent can pre-assign one.
func NewID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("wf_%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(b))
}

// Create makes a new run directory and copies the script into it. A preset
// id via DYNA_RUN_ID (set by `dyna run --detach`) is honored.
func Create(name, scriptSrc string, args any) (out *Run, err error) {
	err = sessionlife.WithPublication(context.Background(), RunsDir(), "publication.lock", func() error {
		if e := sessionlife.Activate(SessionsDir(), OwnerFromEnv()); e != nil {
			return e
		}
		out, err = create(name, scriptSrc, args)
		return err
	})
	return out, err
}
func create(name, scriptSrc string, args any) (*Run, error) {
	if name == "" {
		name = "workflow"
	}
	id := os.Getenv("DYNA_RUN_ID")
	if id == "" {
		id = NewID()
	}
	if err := validateRunID(id); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	r := &Run{Meta: Meta{ID: id, Name: name, Status: "running", Pid: os.Getpid(), Args: args, StartedAt: time.Now()}}
	// A run reports and ends itself, so its own start time lets a later check
	// tell it from a process that reuses the PID. A system without /proc keeps
	// zero and the plain PID probe.
	if started, err := procident.StartTime(r.Meta.Pid); err == nil {
		r.Meta.PidStart = started
	}
	if session := OwnerFromEnv(); session != "" {
		r.Meta.Session = session
		if os.Getenv(ModeEnv) == "subagent" && IsSessionDirID(session) {
			r.Meta.ParentSession = session
		}
	}
	if stamp, ok := mode.StampFromEnv(); ok {
		r.Meta.RoomID = stamp.RoomID
		r.Meta.Room = stamp.Room
	}
	r.Meta.Detached = os.Getenv("DYNA_RUN_DETACHED") == "1"
	r.Dir = filepath.Join(RunsDir(), r.Meta.ID)
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "script.js"), []byte(scriptSrc), 0o644); err != nil {
		return nil, err
	}
	var err error
	// A run reopened through DYNA_RUN_ID may already hold entries that no
	// directory list covers. Only a fresh run starts the list.
	fresh := true
	for _, name := range []string{"events.jsonl", "journal.jsonl"} {
		if info, err := os.Stat(filepath.Join(r.Dir, name)); err == nil && info.Size() > 0 {
			fresh = false
		}
	}
	// Readers expect both logs to exist from the start.
	for _, name := range []string{"events.jsonl", "journal.jsonl"} {
		if err = createLog(filepath.Join(r.Dir, name)); err != nil {
			return nil, err
		}
	}
	// Only a fresh run starts the directory list; a reopened run keeps the
	// list it has, if any.
	list := filepath.Join(r.Dir, DirsFile)
	if fresh {
		if err := createLog(list); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(list); err == nil {
		r.dirs = list
	}
	r.dirsSeen = map[string]bool{}
	if err := r.saveMeta(); err != nil {
		return nil, err
	}
	return r, nil
}

// saveMeta stores the run's meta in its row.
func (r *Run) saveMeta() error {
	_, err := changeMeta(r.Dir, r.Meta.ID, func(ctx context.Context, conn *sql.Conn) error { return upsertMeta(ctx, conn, r.Meta) })
	return err
}

// SetWriters attaches the coordinator before workflow artifacts are written.
// Callbacks run under the run lock and must not re-enter this Run.
func (r *Run) SetWriters(record func([]string) error, finish func(callspec.RunFinish) (Meta, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordDirectories, r.finishRun = record, finish
	r.dirsSeen = map[string]bool{}
}

// recordDirs adds worker directories to the run's list once each, before the
// entry that names them is written. The caller holds r.mu. A list that misses
// a directory would hide an owner from cleanup, so a failed write deletes the
// list and owner lookup reads the journals instead. It reports false while a
// stale list cannot be deleted and the entry names a directory: the caller
// must then not write the entry.
func (r *Run) recordDirs(dirs ...string) bool {
	if r.recordDirectories != nil {
		var pending []string
		for _, dir := range dirs {
			if dir != "" && !r.dirsSeen[dir] {
				pending = append(pending, dir)
			}
		}
		if len(pending) == 0 {
			return true
		}
		var err error
		if r.finishing {
			err = fmt.Errorf("run is already finishing")
		} else {
			err = r.recordDirectories(pending)
		}
		if err != nil {
			r.directoryErr = errors.Join(r.directoryErr, err)
			return false
		}
	}
	// TODO: legacy/shim remove later — retain the directory index for the current
	// worktree ownership reader until it consumes canonical SQLite evidence.
	// Coordinator writes above acknowledge that evidence before this copy.
	for _, dir := range dirs {
		if r.dirsStale {
			break
		}
		if r.dirs == "" || dir == "" || r.dirsSeen[dir] {
			continue
		}
		b, _ := json.Marshal(dir)
		if appendLog(r.dirs, b) == nil {
			r.dirsSeen[dir] = true
			continue
		}
		r.dirs, r.dirsStale = "", true
	}
	if r.dirsStale {
		err := os.Remove(filepath.Join(r.Dir, DirsFile))
		if err != nil && !os.IsNotExist(err) {
			// Only an entry that names a directory could hide an owner.
			for _, dir := range dirs {
				if dir != "" {
					fmt.Fprintf(os.Stderr, "dyna: run %s: cannot delete incomplete %s, so this entry is not written: %v\n", r.Meta.ID, DirsFile, err)
					return false
				}
			}
			return true
		}
		r.dirsStale = false
	}
	return true
}

func (r *Run) Append(e Event) {
	e.TS = time.Now().UnixMilli()
	b, _ := json.Marshal(e)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.recordDirs(e.Dir) {
		return
	}
	_ = appendLog(filepath.Join(r.Dir, "events.jsonl"), b)
	if e.Workflow != "" {
		_ = appendLog(filepath.Join(r.workflowDir(e.Workflow), "events.jsonl"), b)
	}
}

// Journal records the full prompt/result of one agent call.
func (r *Run) Journal(e JournalEntry) {
	b, _ := json.Marshal(e)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.recordDirs(e.Dir, e.Cwd) {
		return
	}
	_ = appendLog(filepath.Join(r.Dir, "journal.jsonl"), b)
	if e.Workflow != "" {
		_ = appendLog(filepath.Join(r.workflowDir(e.Workflow), "journal.jsonl"), b)
	}
}

func (r *Run) workflowDir(id string) string {
	return filepath.Join(r.Dir, "workflows", id)
}

func validateWorkflowID(id string) error {
	if !strings.HasPrefix(id, "nested-") || len(id) == len("nested-") {
		return fmt.Errorf("invalid nested workflow id %q", id)
	}
	for _, c := range id[len("nested-"):] {
		if c < '0' || c > '9' {
			return fmt.Errorf("invalid nested workflow id %q", id)
		}
	}
	return nil
}

// StartWorkflow creates the child artifact directory. Its event and completion
// ledgers are mirrors of the annotated records in the parent run. The parent
// run's workflow_start and workflow_end events carry its name, phase and
// status.
func (r *Run) StartWorkflow(id, script string) error {
	if r == nil || r.Dir == "" {
		return fmt.Errorf("run has no directory")
	}
	if err := validateWorkflowID(id); err != nil {
		return err
	}
	dir := r.workflowDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create nested workflow directory: %w", err)
	}
	// Created exclusively: starting the same sub-workflow twice fails here.
	f, err := os.OpenFile(filepath.Join(dir, "script.js"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	for _, file := range []string{"events.jsonl", "journal.jsonl"} {
		if err := createLog(filepath.Join(dir, file)); err != nil {
			return err
		}
	}
	return nil
}

// FinishWorkflow writes the nested result, if there is one.
func (r *Run) FinishWorkflow(id, resultJSON string) error {
	if err := validateWorkflowID(id); err != nil {
		return err
	}
	if resultJSON == "" {
		return nil
	}
	return os.WriteFile(filepath.Join(r.workflowDir(id), "result.json"), []byte(resultJSON+"\n"), 0o644)
}

// appendLog adds one line to a run log, on disk before it returns: a run's
// events, journal and directory list must survive a crash. Run logs publish
// nothing while roomsvc's run scan still covers runs.
func appendLog(path string, line []byte) error {
	db, err := runsDB()
	if err != nil {
		return err
	}
	return state.For(db).AppendSynced(context.Background(), hub.Event{}, path, line)
}

// createLog creates an empty run log through the one append path, so the
// write ratchet does not count it: AppendRecord with no line opens the file
// with O_CREATE and writes nothing.
func createLog(path string) error {
	db, err := runsDB()
	if err != nil {
		return err
	}
	return state.For(db).AppendRecord(context.Background(), hub.Event{}, path, func(io.ReaderAt, int64) ([]byte, error) { return nil, nil })
}

func validateAgentID(id int) error {
	if id <= 0 {
		return fmt.Errorf("agent id must be a positive integer")
	}
	return nil
}

func agentIDFromJournalPath(path string) (int, error) {
	return agentIDFromJournalPathAtRoot(path, "")
}

func agentIDFromJournalPathAtRoot(path, root string) (int, error) {
	if path == "" || !filepath.IsAbs(path) {
		return 0, fmt.Errorf("agent journal path must be absolute")
	}
	if filepath.Clean(path) != path {
		return 0, fmt.Errorf("agent journal path must be canonical")
	}
	if filepath.Base(path) != "journal.jsonl" {
		return 0, fmt.Errorf("agent journal path must end in journal.jsonl")
	}
	agentDir := filepath.Dir(path)
	agentIDText := filepath.Base(agentDir)
	agentID, err := strconv.Atoi(agentIDText)
	if err != nil || agentID <= 0 || strconv.Itoa(agentID) != agentIDText {
		return 0, fmt.Errorf("agent journal path must contain a positive numeric agent id")
	}
	agentsDir := filepath.Dir(agentDir)
	if filepath.Base(agentsDir) != "agents" {
		return 0, fmt.Errorf("agent journal path must use an agents/<id>/journal.jsonl layout")
	}
	ownerID := filepath.Base(filepath.Dir(agentsDir))
	if IsSessionDirID(ownerID) {
		// A subagent session's own journal: sessions/<id>/agents/<n>/journal.jsonl.
	} else if err := validateRunID(ownerID); err != nil {
		return 0, fmt.Errorf("agent journal path: %w", err)
	}
	runsRoot, err := agentJournalValidationRoot(ownerID, root)
	if err != nil {
		return 0, fmt.Errorf("resolve runs directory: %w", err)
	}
	rel, err := filepath.Rel(runsRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, fmt.Errorf("agent journal path must be inside %s", runsRoot)
	}
	return agentID, nil
}

func validateAgentJournalParent(path string) error {
	return validateAgentJournalParentAtRoot(path, "")
}

func validateAgentJournalParentAtRoot(path, root string) error {
	ownerID := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	runsRoot, err := agentJournalValidationRoot(ownerID, root)
	if err != nil {
		return fmt.Errorf("resolve runs directory: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(runsRoot)
	if err != nil {
		return fmt.Errorf("resolve runs directory: %w", err)
	}
	parent := filepath.Dir(path)
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve agent journal directory: %w", err)
	}
	rel, err := filepath.Rel(runsRoot, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("agent journal directory must be inside %s", runsRoot)
	}
	if filepath.Clean(parentReal) != filepath.Clean(filepath.Join(rootReal, rel)) {
		return fmt.Errorf("agent journal directory must not traverse a symlink")
	}
	return nil
}

func agentJournalValidationRoot(ownerID, root string) (string, error) {
	if root == "" {
		root = strings.TrimSpace(os.Getenv(AgentJournalRootEnv))
	}
	if root == "" {
		root = RunsDir()
		if IsSessionDirID(ownerID) {
			root = SessionsDir()
		}
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("agent journal validation root must be absolute")
	}
	root = filepath.Clean(root)
	return root, nil
}

func validateAgentJournalFile(path string) error {
	return validateAgentJournalFileAtRoot(path, "")
}

func validateAgentJournalFileAtRoot(path, root string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return err
		}
		return fmt.Errorf("inspect agent journal: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("agent journal must be a regular file, not a symlink or special file")
	}
	if err := validateAgentJournalParentAtRoot(path, root); err != nil {
		return err
	}
	return nil
}

// ReadJournal parses journal.jsonl for a run (resume cache source).
func ReadJournal(id string) ([]JournalEntry, error) {
	records, _, err := readJSONLines[JournalEntry](filepath.Join(RunDir(id), "journal.jsonl"), 0, 0, 16*1024*1024, jsonLinesPolicy{acceptFinal: true, errorOversized: true})
	if err != nil {
		if openErr, ok := err.(*os.PathError); ok && openErr.Op == "open" {
			return nil, err
		}
		return nil, fmt.Errorf("read journal %s: %w", id, err)
	}
	return records, nil
}

// JournalResult returns one worker's result from the run journal as the JSON
// text it was written with, so its key order stays as the worker wrote it.
// A worker the journal names twice (a retry) gives its last result. It reads
// one record at a time with no record size limit, so one large result never
// hides another worker's; callers apply their own result limit.
func JournalResult(id string, agent int) (string, bool, error) {
	f, err := os.Open(filepath.Join(RunDir(id), "journal.jsonl"))
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	type entry struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	r := bufio.NewReader(f)
	out, found := "", false
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e entry
			// A torn last record is skipped, as other journal readers do.
			if json.Unmarshal(line, &e) == nil && e.ID == agent {
				out, found = string(e.Result), true
			}
		}
		if err == io.EOF {
			return out, found, nil
		}
		if err != nil {
			return "", false, err
		}
	}
}

// ReadJournalFrom parses complete records appended to journal.jsonl starting
// at offset. The returned offset is the byte immediately after the last
// newline-terminated record and can be passed to the next call. A partial
// trailing record is left uncommitted until a later call observes its newline.
func ReadJournalFrom(id string, offset int64) ([]JournalEntry, int64, error) {
	return readJSONLinesFrom[JournalEntry](filepath.Join(RunDir(id), "journal.jsonl"), offset, 64*1024*1024)
}

// ReadMeta reads one run's meta from dyna.db.
func ReadMeta(id string) (Meta, error) {
	return ReadMetaIn(RunsDir(), id)
}

// ReadMetaIn reads a run from the SQLite store beside runsRoot. The existing
// legacy importer handles a meta.json written by an earlier release once.
func ReadMetaIn(runsRoot, id string) (Meta, error) {
	if err := ValidateRunID(id); err != nil {
		return Meta{}, err
	}
	db, err := openRuns(filepath.Dir(runsRoot))
	if err != nil {
		return Meta{}, err
	}
	return metaAt(db, filepath.Join(runsRoot, id), id)
}

// Finish writes the result artifact and returns only after terminal state is acknowledged.
func (r *Run) Finish(status string, resultJSON string, runErr error) error {
	var artifactErr error
	if resultJSON != "" {
		if err := os.WriteFile(filepath.Join(r.Dir, "result.json"), []byte(resultJSON+"\n"), 0o644); err != nil {
			artifactErr = fmt.Errorf("write run result: %w", err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishing = true
	completionErr := errors.Join(artifactErr, r.directoryErr)
	errMsg := ""
	if combined := errors.Join(runErr, completionErr); combined != nil {
		errMsg = combined.Error()
	}
	if completionErr != nil && status == "ok" {
		status = "error"
	}
	var meta Meta
	var err error
	if r.finishRun != nil {
		meta, err = r.finishRun(callspec.RunFinish{Run: r.Meta.ID, Status: status, Error: errMsg, EndedAt: time.Now(), DirectoriesComplete: r.directoryErr == nil})
	} else {
		// TODO: legacy/shim remove later — standalone callers still finalize locally;
		// workflow coordinators always attach the engine writer before execution.
		meta, _, err = endRun(r.Dir, r.Meta.ID, status, errMsg, time.Now())
	}
	if err != nil {
		return errors.Join(completionErr, fmt.Errorf("finish run: %w", err))
	}
	r.Meta = meta
	return completionErr
}

// List returns metadata for all live runs, newest first. Archived runs, and a
// runs/<id> pointer an earlier release left, are not scanned; see
// ListArchived.
func List() ([]Meta, error) { return listIn(RunsDir(), "1") }

// ReadEvents parses events.jsonl for a run.
func ReadEvents(id string) ([]Event, error) {
	records, _, err := readJSONLines[Event](filepath.Join(RunDir(id), "events.jsonl"), 0, 0, 4*1024*1024, jsonLinesPolicy{acceptFinal: true, errorOversized: true})
	if err != nil {
		if openErr, ok := err.(*os.PathError); ok && openErr.Op == "open" {
			return nil, err
		}
		return nil, fmt.Errorf("read events %s: %w", id, err)
	}
	return records, nil
}

// ReadEventsFrom parses complete records appended to events.jsonl starting at
// offset. The returned offset is the byte immediately after the last
// newline-terminated record and can be passed to the next call. A partial
// trailing record is left uncommitted until a later call observes its newline.
func ReadEventsFrom(id string, offset int64) ([]Event, int64, error) {
	return readJSONLinesFrom[Event](filepath.Join(RunDir(id), "events.jsonl"), offset, 4*1024*1024)
}

func readJSONLinesFrom[T any](path string, offset int64, maxRecordBytes int) ([]T, int64, error) {
	return readJSONLinesLimitedFrom[T](path, offset, 0, maxRecordBytes)
}

func readJSONLinesLimitedFrom[T any](path string, offset int64, limit int64, maxRecordBytes int) ([]T, int64, error) {
	return readJSONLines[T](path, offset, limit, maxRecordBytes, jsonLinesPolicy{})
}

type jsonLinesPolicy struct {
	keepLast       int
	acceptFinal    bool
	errorOversized bool
}

func readJSONLines[T any](path string, offset int64, limit int64, maxRecordBytes int, policy jsonLinesPolicy, keep ...func(T) bool) ([]T, int64, error) {
	return readJSONLinesDecoded(path, offset, limit, maxRecordBytes, policy, func(_, _ int64, line []byte) (record T, ok bool, err error) {
		return record, json.Unmarshal(line, &record) == nil, nil
	}, keep...)
}

func readJSONLinesDecoded[T any](path string, offset, limit int64, maxRecordBytes int, policy jsonLinesPolicy, decode func(int64, int64, []byte) (T, bool, error), keep ...func(T) bool) ([]T, int64, error) {
	var out, kept []T
	oldest := 0
	next, err := visitJSONLines(path, offset, limit, maxRecordBytes, policy, func(start, end int64, line []byte) error {
		record, ok, err := decode(start, end, line)
		if err != nil {
			return err
		}
		if ok {
			if policy.keepLast > 0 && len(out) == policy.keepLast {
				if len(keep) > 0 && keep[0](out[oldest]) {
					kept = append(kept, out[oldest])
				}
				out[oldest] = record
				oldest = (oldest + 1) % len(out)
			} else {
				out = append(out, record)
			}
		}
		return nil
	})
	if oldest > 0 {
		out = append(out[oldest:len(out):len(out)], out[:oldest]...)
	}
	return append(kept, out...), next, err
}

// VisitNormalizedOutputLines visits complete source lines with original offsets.
// Oversized lines have nil bytes. Partial trailing lines remain uncommitted.
func VisitNormalizedOutputLines(path string, offset, limit int64, visit func(projection.Line) error) (int64, error) {
	return visitJSONLines(path, offset, limit, normalizedOutputMaxRecordBytes, jsonLinesPolicy{}, func(start, end int64, raw []byte) error {
		return visit(projection.Line{Start: start, End: end, Raw: raw})
	})
}

func visitJSONLines(path string, offset, limit int64, maxRecordBytes int, policy jsonLinesPolicy, visit func(int64, int64, []byte) error) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return offset, err
	}
	// Run files are append-only. If a retained offset lies past EOF (for
	// example after an unexpected truncation), restart from the beginning.
	// Replacements that have already regrown past offset are intentionally not
	// detected; normal run lifecycle never replaces these files.
	if offset < 0 || offset > info.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}

	next := offset
	var src io.Reader = f
	if limit > 0 {
		src = io.LimitReader(f, limit)
	}
	r := bufio.NewReaderSize(src, 64*1024)
	line := make([]byte, 0, min(maxRecordBytes, 64*1024))
	lineBytes := 0
	oversized := false
	for {
		fragment, readErr := r.ReadSlice('\n')
		lineBytes += len(fragment)
		if !oversized {
			if lineBytes > maxRecordBytes {
				if policy.errorOversized {
					return next, bufio.ErrTooLong
				}
				// Bound memory for corrupt or unexpectedly huge records. A
				// newline-terminated oversized record is committed and skipped,
				// just like other malformed complete records.
				oversized = true
				line = line[:0]
			} else {
				line = append(line, fragment...)
			}
		}
		if readErr == bufio.ErrBufferFull {
			continue
		}
		// Scanner-based legacy reads need room beyond a full buffer to
		// observe EOF for an unterminated record.
		if policy.errorOversized && readErr == io.EOF && lineBytes >= maxRecordBytes {
			return next, bufio.ErrTooLong
		}
		if (len(fragment) > 0 && fragment[len(fragment)-1] == '\n') || (policy.acceptFinal && readErr == io.EOF && lineBytes > 0) {
			// A malformed complete record is committed too: advancing past it
			// avoids attempting to decode it on every subsequent poll.
			var raw []byte
			if !oversized {
				raw = line
			}
			if err := visit(next, next+int64(lineBytes), raw); err != nil {
				return next, err
			}
			next += int64(lineBytes)
			line = line[:0]
			lineBytes = 0
			oversized = false
		}
		if readErr != nil {
			if readErr == io.EOF {
				return next, nil
			}
			return next, readErr
		}
	}
}

// ReadResult returns result.json contents if present.
func ReadResult(id string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(RunDir(id), "result.json"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// ---- lifecycle management (cancel / pause / remove) ----

func pausePath(id string) string { return filepath.Join(RunsDir(), id, "paused") }

// Canceling reports whether run id's cancellation has started: the run is
// marked canceling, or it already ended as canceled. Shell publication
// refuses such a run, so a worker cannot launch a shell behind the cancel
// cleanup's snapshot (D-165).
func Canceling(id string) bool {
	m, err := ReadMeta(id)
	return err == nil && (m.Canceling || m.Status == "canceled")
}

// SetPaused pauses or resumes a running run in its row: while paused, the
// runner starts no new workers, and running workers finish. A run that is
// not running does not change.
func SetPaused(id string, paused bool) error {
	return setPaused(RunDir(id), id, paused)
}

// Cancel stops a running workflow: signals its process (the run's SIGTERM
// handler cancels the context, which kills worker process groups). If the
// process is already gone (crash, reboot), the run is finalized as canceled.
func Cancel(id string) error {
	m, err := ReadMeta(id)
	if err != nil {
		return err
	}
	if m.Status != "running" {
		return fmt.Errorf("run %s is not running (status %s)", id, m.Status)
	}
	SetPaused(id, false) // a paused run must be unblocked to observe cancellation
	// Fence new worker shells before the signal: publication refuses a run
	// whose cancellation has started, so no supervisor launches behind the
	// cancel cleanup's snapshot (D-165).
	if _, err := setCanceling(RunDir(id), id); err != nil {
		return err
	}
	if Alive(m) {
		return syscall.Kill(m.Pid, syscall.SIGTERM)
	}
	return ForceStatus(id, "canceled", "canceled (process no longer alive)")
}

// ForceStatus ends a running run that lost its process (stale-run
// cleanup). A run that already ended keeps its status.
func ForceStatus(id, status, errMsg string) error {
	if _, err := ReadMeta(id); err != nil {
		return err
	}
	_, _, err := endRun(RunDir(id), id, status, errMsg, time.Now())
	return err
}

// Remove deletes a run's directory (and, for an archived run, its archive
// directory and pointer). Running runs must be canceled first.
func Remove(id string) error {
	return RemoveWith(id, nil)
}

// RemoveWith checks that a run can be removed before calling beforeRemove.
// A failed hook leaves the run's storage in place.
func RemoveWith(id string, beforeRemove func(Meta) error) error {
	if err := ValidateRunID(id); err != nil {
		return err
	}
	m, err := ReadMeta(id)
	if err != nil {
		return err
	}
	if runIsAlive(m) {
		return fmt.Errorf("run %s is still running; cancel it first", id)
	}
	if beforeRemove != nil {
		if err := beforeRemove(m); err != nil {
			return err
		}
	}
	// Retention never unlinks a live entry it cannot prove is this archive's
	// pointer, and it checks that before the canonical row goes.
	if _, err := archivedPointer(id); err != nil {
		return err
	}
	// The row goes in one write; the folder, which holds only files, after
	// it commits (item 5).
	db, err := runsDB()
	if err != nil {
		return err
	}
	if err := deleteMeta(db, id); err != nil {
		return err
	}
	if err := removeArchived(id); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(RunsDir(), id)); err != nil {
		return err
	}
	return removeRunLocks(id)
}

func runIsAlive(m Meta) bool {
	return m.Status == "running" && Alive(m)
}

// ClearCompletedInRoom limits completed-run clearing to roomID. An empty
// roomID keeps the unfiltered behavior used by owner and maintenance calls.
func ClearCompletedInRoom(roomID string, beforeRemove func(Meta) error) (int, []error, error) {
	runs, err := List()
	if err != nil {
		return 0, nil, err
	}
	n := 0
	var failures []error
	for _, r := range runs {
		if roomID != "" && r.RoomID != roomID {
			continue
		}
		if runIsAlive(r) {
			continue
		}
		if err := RemoveWith(r.ID, beforeRemove); err != nil {
			failures = append(failures, fmt.Errorf("remove run %s: %w", r.ID, err))
			continue
		}
		n++
	}
	return n, failures, nil
}
