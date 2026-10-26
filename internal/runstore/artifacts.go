package runstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/store"
	"dyna-agent/internal/workeroutput"
)

// AgentJournalPath returns the canonical absolute journal path for an agent in
// a stored run. Both identifiers are validated before any filesystem access.
func AgentJournalPath(runID string, agentID int) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	if err := validateAgentID(agentID); err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(RunsDir(), runID, "agents", strconv.Itoa(agentID), "journal.jsonl"))
}

// AgentOutputPath returns the append-only live harness stdout for one agent.
func AgentOutputPath(runID string, agentID int) (string, error) {
	return agentArtifactPath(runID, agentID, "stdout.log")
}

// AgentNormalizedOutputPath returns the schema-v1 normalized output stream.
func AgentNormalizedOutputPath(runID string, agentID int) (string, error) {
	return agentArtifactPath(runID, agentID, "output.jsonl")
}

// AgentNormalizedOutputKey identifies output across run archive moves.
func AgentNormalizedOutputKey(runID string, agentID int) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	if err := validateAgentID(agentID); err != nil {
		return "", err
	}
	return fmt.Sprintf("runs/%s/agents/%d", runID, agentID), nil
}

// AgentSessionID is the ID of the leaf session that serves agent agentID of
// run runID. It is derived, so a coordinator that restarts finds the session
// an earlier call started. The date and time are the run's own (a run ID is
// wf_<date>-<time>-<hex>), so the agents of a run sort with it; the suffix is
// 16 hex digits of a hash of the run and agent.
func AgentSessionID(runID string, agentID int) string {
	stamp := "20000101-000000"
	if rest, ok := strings.CutPrefix(runID, "wf_"); ok && len(rest) >= len(stamp) {
		if _, err := time.Parse("20060102-150405", rest[:len(stamp)]); err == nil {
			stamp = rest[:len(stamp)]
		}
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s/agent:%d", runID, agentID))
	return SessionDirPrefix + stamp + "-" + hex.EncodeToString(sum[:8])
}

// agentArtifactPath is where one agent's output artifact lives. A run that
// kept the artifact in its own directory (runs before agents became sessions)
// still reads it there; otherwise the agent's session holds it, in the first
// and only turn's output files.
func agentArtifactPath(runID string, agentID int, name string) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	if err := validateAgentID(agentID); err != nil {
		return "", err
	}
	own := filepath.Join(RunDir(runID), "agents", strconv.Itoa(agentID), name)
	if _, err := os.Lstat(own); err != nil {
		session := filepath.Join(SessionsDir(), AgentSessionID(runID, agentID), "agents", "1", name)
		if _, serr := os.Lstat(session); serr == nil {
			return filepath.Abs(session)
		}
	}
	return filepath.Abs(own)
}

func (r *Run) agentJournalPath(agentID int) (string, error) {
	if r == nil || r.Dir == "" {
		return "", fmt.Errorf("run has no directory")
	}
	if err := validateAgentID(agentID); err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(r.Dir, "agents", strconv.Itoa(agentID), "journal.jsonl"))
	if err != nil {
		return "", err
	}
	if _, err := agentIDFromJournalPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// StartAgentJournal creates an agent journal and writes its first, system-
// authored record. Prompt is stored in full; it is deliberately not the
// truncated preview used by events.jsonl.
func (r *Run) StartAgentJournal(agentID int, label, profile, phase, prompt string) (string, error) {
	path, err := r.agentJournalPath(agentID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create agent journal directory: %w", err)
	}
	entry := AgentJournalEntry{
		TS: time.Now().UnixMilli(), Kind: "start", Message: "Agent started", Source: "system",
		AgentID: agentID, Label: label, Profile: profile, Phase: phase, Prompt: prompt,
	}
	if err := writeAgentJournalRecord(path, entry, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY); err != nil {
		return "", err
	}
	return path, nil
}

// AppendAgentJournal appends a timestamped, system-authored record to an
// existing agent journal.
func (r *Run) AppendAgentJournal(agentID int, entry AgentJournalEntry) error {
	path, err := r.agentJournalPath(agentID)
	if err != nil {
		return err
	}
	entry.TS = time.Now().UnixMilli()
	entry.Source = "system"
	return AppendAgentJournalPath(path, entry)
}

// AppendAgentJournalPath appends a caller-authored record to an existing
// absolute agent-journal path, adding the current timestamp when TS is zero.
// The complete JSONL record is issued in one O_APPEND write so concurrent
// worker and monitor entries cannot overwrite one another.
func AppendAgentJournalPath(path string, entry AgentJournalEntry) error {
	if entry.TS == 0 {
		entry.TS = time.Now().UnixMilli()
	}
	return writeAgentJournalRecord(path, entry, os.O_APPEND|os.O_WRONLY)
}

// AppendAgentJournalEntryFromEnv appends agent progress.
// The worker cannot override the timestamp or source.
func AppendAgentJournalEntryFromEnv(entry AgentJournalEntry) error {
	path := os.Getenv(AgentJournalEnv)
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s is not set; `dyna journal` is only available inside a dyna worker", AgentJournalEnv)
	}
	entry.Kind = strings.TrimSpace(entry.Kind)
	switch entry.Kind {
	case "update", "finding", "decision", "verification", "blocker":
	default:
		return fmt.Errorf("agent journal kind must be one of: update, finding, decision, verification, blocker")
	}
	entry.TS = time.Now().UnixMilli()
	entry.Source = "agent"
	return writeAgentJournalRecord(path, entry, os.O_APPEND|os.O_WRONLY)
}

// ReadAgentJournalFrom parses complete agent-journal records starting at
// offset. Invalid complete records are skipped and committed; a partial
// trailing record remains uncommitted until its newline arrives.
func ReadAgentJournalFrom(runID string, agentID int, offset int64) ([]AgentJournalEntry, int64, error) {
	if dir, ok := archivedRunDir(runID); ok {
		return readArchivedAgentJournalFrom(dir, agentID, offset)
	}
	path, err := AgentJournalPath(runID, agentID)
	if err != nil {
		return nil, offset, err
	}
	return ReadAgentJournalPathFrom(path, offset)
}

// ReadAgentJournalPathFrom is ReadAgentJournalFrom for the absolute path held
// by a harness monitor.
func ReadAgentJournalPathFrom(path string, offset int64) ([]AgentJournalEntry, int64, error) {
	return readAgentJournalPathFromRoot(path, "", offset)
}

// ReadAgentJournalPathFromRoot reads a journal under an explicit trusted store
// root. It applies the same layout and symlink checks as the default reader.
// The root must be absolute and is independent of worker environment settings.
func ReadAgentJournalPathFromRoot(path, root string, offset int64) ([]AgentJournalEntry, int64, error) {
	if !filepath.IsAbs(root) {
		return nil, offset, fmt.Errorf("agent journal validation root must be absolute")
	}
	return readAgentJournalPathFromRoot(path, root, offset)
}

func readAgentJournalPathFromRoot(path, root string, offset int64) ([]AgentJournalEntry, int64, error) {
	if _, err := agentIDFromJournalPathAtRoot(path, root); err != nil {
		return nil, offset, err
	}
	if err := validateAgentJournalFileAtRoot(path, root); err != nil {
		return nil, offset, err
	}
	return readJSONLinesFrom[AgentJournalEntry](path, offset, agentJournalMaxRecordBytes)
}

// readArchivedAgentJournalFrom reads an archived run's agent journal with the
// same no-symlink rules the live path enforces, relative to the archive.
func readArchivedAgentJournalFrom(runDir string, agentID int, offset int64) ([]AgentJournalEntry, int64, error) {
	if err := validateAgentID(agentID); err != nil {
		return nil, offset, err
	}
	path := filepath.Join(runDir, "agents", strconv.Itoa(agentID), "journal.jsonl")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, offset, err
	}
	if !info.Mode().IsRegular() {
		return nil, offset, fmt.Errorf("agent journal must be a regular file, not a symlink or special file")
	}
	rootReal, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return nil, offset, err
	}
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, offset, err
	}
	if parentReal != filepath.Join(rootReal, "agents", strconv.Itoa(agentID)) {
		return nil, offset, fmt.Errorf("agent journal directory must not traverse a symlink")
	}
	return readJSONLinesFrom[AgentJournalEntry](path, offset, agentJournalMaxRecordBytes)
}

// ReadAgentOutputFrom reads raw stdout bytes appended after offset.
func ReadAgentOutputFrom(runID string, agentID int, offset int64) (string, int64, error) {
	b, next, err := ReadAgentOutputLimitedFrom(runID, agentID, offset, 0)
	return string(b), next, err
}

// ReadAgentOutputLimitedFrom reads up to limit raw stdout bytes after offset.
// A limit of 0 is unbounded. The returned offset can be passed to a later call.
func ReadAgentOutputLimitedFrom(runID string, agentID int, offset, limit int64) ([]byte, int64, error) {
	var buf bytes.Buffer
	_, next, err := CopyAgentOutputTo(&buf, runID, agentID, offset, limit)
	if err != nil {
		return nil, next, err
	}
	return buf.Bytes(), next, nil
}

// CopyAgentOutputTo streams raw stdout bytes after offset into w without
// requiring the caller to buffer the whole artifact. A limit of 0 is unbounded.
func CopyAgentOutputTo(w io.Writer, runID string, agentID int, offset, limit int64) (int64, int64, error) {
	path, err := AgentOutputPath(runID, agentID)
	if err != nil {
		return 0, offset, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, offset, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, offset, err
	}
	if offset < 0 || offset > info.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, offset, err
	}
	var r io.Reader = f
	if limit > 0 {
		r = io.LimitReader(f, limit)
	}
	written, err := io.Copy(w, r)
	return written, offset + written, err
}

// ReadAgentNormalizedOutputFrom parses complete normalized output records
// starting at offset. A partial trailing record remains uncommitted.
func ReadAgentNormalizedOutputFrom(runID string, agentID int, offset int64) ([]workeroutput.Event, int64, error) {
	return ReadAgentNormalizedOutputLimitedFrom(runID, agentID, offset, 0)
}

// ReadAgentNormalizedOutputLimitedFrom parses complete normalized output
// records starting at offset, scanning at most limit artifact bytes. A limit
// of 0 reads to EOF. A record cut by the limit is left uncommitted, the same
// as a live partial tail.
func ReadAgentNormalizedOutputLimitedFrom(runID string, agentID int, offset, limit int64) ([]workeroutput.Event, int64, error) {
	path, err := AgentNormalizedOutputPath(runID, agentID)
	if err != nil {
		return nil, offset, err
	}
	key, err := AgentNormalizedOutputKey(runID, agentID)
	if err != nil {
		return nil, offset, err
	}
	return ReadProjectedNormalizedOutputPath(store.DataDir(), key, path, offset, limit)
}

func writeExclusiveFile(path string, contents []byte) error {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		syscall.Close(fd)
		return fmt.Errorf("open %s", path)
	}
	if _, err := f.Write(contents); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
