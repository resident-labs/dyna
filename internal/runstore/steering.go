package runstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// steeringSchema holds each worker's steering state (rule r10). queue_start
// is the size of steering.jsonl when the worker last activated: a read starts
// there, so activating never rewrites the queue.
const steeringSchema = `CREATE TABLE run_agent_steering (
 run TEXT NOT NULL,
 agent INTEGER NOT NULL,
 pid INTEGER NOT NULL,
 started INTEGER NOT NULL,
 active INTEGER NOT NULL,
 queue_start INTEGER NOT NULL,
 PRIMARY KEY(run, agent)
);`

// migrateSteering creates the steering table. openRuns calls it, so it
// migrates with the runs table.
func migrateSteering(db *sql.DB) error {
	return database.Migrate(db, "runs.steering", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), steeringSchema)
		return err
	})
}

func agentSteeringDir(runID string, agentID int) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	if err := validateAgentID(agentID); err != nil {
		return "", err
	}
	return filepath.Join(RunsDir(), runID, "agents", strconv.Itoa(agentID)), nil
}

// steeringKey names a worker's steering-state.json copy for the rollback
// helper.
func steeringKey(runID string, agentID int) string {
	return "runs/" + runID + "/agents/" + strconv.Itoa(agentID) + "/steering"
}

// withAgentSteeringLock holds a worker's steering lock across processes: the
// worker, the CLI and the engine all change its state, so each imports,
// writes and copies it under this lock.
func withAgentSteeringLock(runID string, agentID int, fn func(string) error) error {
	dir, err := agentSteeringDir(runID, agentID)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(dir); err != nil {
		return fmt.Errorf("agent %d is not active in run %s", agentID, runID)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("agent steering directory must be a real directory")
	}
	lockPath := filepath.Join(dir, "steering.lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("agent steering lock must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	unlock, err := store.Lock(lockPath)
	if err != nil {
		return fmt.Errorf("lock agent steering queue: %w", err)
	}
	defer unlock()
	return fn(dir)
}

// errNoSteering is a worker with no steering state.
var errNoSteering = errors.New("worker does not support live steering or is no longer active")

// readSteeringState imports a steering-state.json that an earlier release
// wrote, then reads the worker's row. The caller holds the steering lock.
func readSteeringState(dir, runID string, agentID int) (steeringState, error) {
	var s steeringState
	db, err := runsDB()
	if err != nil {
		return s, err
	}
	ctx := context.Background()
	path := filepath.Join(dir, "steering-state.json")
	if _, err := state.For(db).ImportIfForeign(ctx, steeringKey(runID, agentID), path, importSteering(runID, agentID)); err != nil {
		return s, err
	}
	var active int
	err = db.QueryRowContext(ctx, "SELECT pid, started, active, queue_start FROM run_agent_steering WHERE run=? AND agent=?", runID, agentID).
		Scan(&s.Pid, &s.StartedAt, &active, &s.QueueStart)
	if errors.Is(err, sql.ErrNoRows) {
		return s, errNoSteering
	}
	s.RunID, s.AgentID, s.Active = runID, agentID, active != 0
	return s, err
}

// TODO: legacy/shim remove later — reads a steering-state.json that an earlier release's worker or CLI wrote; remove with item 31, when no run from before this release is alive.
func importSteering(runID string, agentID int) func(*sql.Conn, []byte) error {
	return func(conn *sql.Conn, raw []byte) error {
		var s steeringState
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("run %s agent %d: steering-state.json: %v: %w", runID, agentID, err, state.ErrSkipForeign)
		}
		if s.RunID != runID || s.AgentID != agentID {
			return fmt.Errorf("run %s agent %d: steering-state.json names run %s agent %d: %w", runID, agentID, s.RunID, s.AgentID, state.ErrSkipForeign)
		}
		// An earlier release truncates the queue when it activates and
		// writes no queueStart, so its file reads from the start.
		return putSteering(context.Background(), conn, s)
	}
}

func putSteering(ctx context.Context, conn *sql.Conn, s steeringState) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO run_agent_steering(run,agent,pid,started,active,queue_start) VALUES(?,?,?,?,?,?)
ON CONFLICT(run,agent) DO UPDATE SET pid=excluded.pid, started=excluded.started, active=excluded.active, queue_start=excluded.queue_start`,
		s.RunID, s.AgentID, s.Pid, s.StartedAt, s.Active, s.QueueStart)
	return err
}

// writeSteeringState stores the worker's row, then writes the earlier
// release's copy. The caller holds the steering lock and has read the state
// first, which imports an earlier release's file.
func writeSteeringState(dir string, s steeringState) error {
	db, err := runsDB()
	if err != nil {
		return err
	}
	w, ctx := state.For(db), context.Background()
	if err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error { return putSteering(ctx, conn, s) }); err != nil {
		return err
	}
	// TODO: legacy/shim remove later — an earlier release's worker and CLI read steering state only from steering-state.json; remove with item 31.
	return w.Mirror(ctx, steeringKey(s.RunID, s.AgentID), filepath.Join(dir, "steering-state.json"), func(out io.Writer) error {
		b, err := json.Marshal(s)
		if err == nil {
			_, err = out.Write(append(b, '\n'))
		}
		return err
	})
}

// steeringQueue is a worker's queue path and its size, 0 when it does not
// exist yet.
func steeringQueue(dir string) (string, int64, error) {
	queue := filepath.Join(dir, "steering.jsonl")
	info, err := os.Lstat(queue)
	if os.IsNotExist(err) {
		return queue, 0, nil
	}
	if err != nil {
		return queue, 0, err
	}
	if !info.Mode().IsRegular() {
		return queue, 0, fmt.Errorf("agent steering queue must be a regular file")
	}
	return queue, info.Size(), nil
}

// ActivateAgentSteering marks a resumable worker as ready to accept messages.
// Only the process recorded as the run owner may activate the queue. Earlier
// messages stay in the queue; reads start after them.
func ActivateAgentSteering(runID string, agentID int) error {
	return withAgentSteeringLock(runID, agentID, func(dir string) error {
		meta, err := ReadMeta(runID)
		if err != nil {
			return err
		}
		if meta.Status != "running" || meta.Pid != os.Getpid() {
			return fmt.Errorf("run %s is not owned by this active process", runID)
		}
		queue, size, err := steeringQueue(dir)
		if err != nil {
			return err
		}
		if err := createLog(queue); err != nil {
			return fmt.Errorf("create agent steering queue: %w", err)
		}
		// Reading first imports an earlier release's file, so the write
		// below is not overwritten by it (the rollback helper's order).
		if _, err := readSteeringState(dir, runID, agentID); err != nil && !errors.Is(err, errNoSteering) {
			return err
		}
		return writeSteeringState(dir, steeringState{
			RunID: runID, AgentID: agentID, Pid: meta.Pid,
			StartedAt: meta.StartedAt.UnixNano(), Active: true, QueueStart: size,
		})
	})
}

// DeactivateAgentSteering closes a worker steering queue. Accepted messages are
// drained atomically before this is called during normal worker completion.
func DeactivateAgentSteering(runID string, agentID int) error {
	return withAgentSteeringLock(runID, agentID, func(dir string) error {
		s, err := readSteeringState(dir, runID, agentID)
		if err != nil {
			return nil
		}
		s.Active = false
		return writeSteeringState(dir, s)
	})
}

// SubmitAgentSteering validates and appends a message at the external command
// boundary. The state check and append share a cross-process lock with worker
// shutdown, so a successful submission cannot race behind deactivation.
func SubmitAgentSteering(runID string, agentID int, message string) error {
	if err := validateRunID(runID); err != nil {
		return err
	}
	if err := validateAgentID(agentID); err != nil {
		return err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("steering message must not be empty")
	}
	if !utf8.ValidString(message) {
		return fmt.Errorf("steering message must be valid UTF-8")
	}
	if len(message) > MaxSteeringMessageBytes {
		return fmt.Errorf("steering message is too long (maximum %s)", settings.FormatBytes(int64(MaxSteeringMessageBytes)))
	}
	return withAgentSteeringLock(runID, agentID, func(dir string) error {
		meta, err := ReadMeta(runID)
		if err != nil {
			return err
		}
		if meta.Status != "running" {
			return fmt.Errorf("run %s is not running (status %s)", runID, meta.Status)
		}
		if !Alive(meta) {
			return fmt.Errorf("run %s is not running (process %d is unavailable)", runID, meta.Pid)
		}
		s, err := readSteeringState(dir, runID, agentID)
		if err != nil {
			return err
		}
		if !s.Active || s.Pid != meta.Pid || s.StartedAt != meta.StartedAt.UnixNano() {
			return fmt.Errorf("agent %d is not an active steerable worker in run %s", agentID, runID)
		}
		queue, _, err := steeringQueue(dir)
		if err != nil {
			return err
		}
		b, err := json.Marshal(SteeringMessage{TS: time.Now().UnixMilli(), Message: message})
		if err != nil {
			return err
		}
		if err := appendLog(queue, b); err != nil {
			return fmt.Errorf("append agent steering message: %w", err)
		}
		return nil
	})
}

// ReadAgentSteeringFrom consumes accepted messages after offset. When
// deactivateIfEmpty is true, the emptiness check and deactivation are atomic
// with submissions; this closes the final completion race.
func ReadAgentSteeringFrom(runID string, agentID int, offset int64, deactivateIfEmpty bool) ([]SteeringMessage, int64, error) {
	var messages []SteeringMessage
	next := offset
	err := withAgentSteeringLock(runID, agentID, func(dir string) error {
		s, err := readSteeringState(dir, runID, agentID)
		if err != nil {
			return err
		}
		if s.Pid != os.Getpid() {
			return fmt.Errorf("agent steering state does not belong to this worker process")
		}
		if !s.Active {
			return nil
		}
		queue, size, err := steeringQueue(dir)
		if err != nil {
			return err
		}
		// A read starts after the messages from before activation. A start
		// past the queue's end means an earlier release truncated it.
		start := max(offset, s.QueueStart)
		if start > size {
			start = 0
		}
		messages, next, err = readJSONLinesFrom[SteeringMessage](queue, start, steeringMaxRecordBytes)
		if err != nil {
			return err
		}
		if deactivateIfEmpty && len(messages) == 0 {
			s.Active = false
			return writeSteeringState(dir, s)
		}
		return nil
	})
	return messages, next, err
}
