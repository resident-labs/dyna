package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// failedStepNames bounds the steps a run's summary names. Failed always holds the true count.
const failedStepNames = 20

// WorkflowRun is one run of a script, tied to the crew session that hosts it. Script, Profiles and Repo never change.
// Commit and Image are set once, by the box's first start. Resume is read only when the run starts.
type WorkflowRun struct {
	ID          contract.WorkflowRunID
	Room        contract.RoomID
	Session     contract.SessionID
	Name        string
	Description string
	Script      string
	Args        string
	Profiles    []contract.ProfileName
	Repo        contract.RepoName
	Commit      string
	Image       string
	State       contract.WorkflowState
	Result      string
	Resume      contract.WorkflowRunID
	Started     time.Time
	Ended       time.Time
}

const workflowColumns = "id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,result,started_at,ended_at"

func scanWorkflow(row interface{ Scan(...any) error }) (WorkflowRun, error) {
	var run WorkflowRun
	var profiles string
	var started, ended int64
	err := row.Scan(&run.ID, &run.Room, &run.Session, &run.Name, &run.Description, &run.Script, &run.Args, &profiles, &run.Repo, &run.Commit, &run.Image, &run.State, &run.Result, &started, &ended)
	if err != nil {
		return run, err
	}
	run.Started = time.UnixMilli(started).UTC()
	if ended != 0 {
		run.Ended = time.UnixMilli(ended).UTC()
	}
	return run, json.Unmarshal([]byte(profiles), &run.Profiles)
}

// insertWorkflow adds a run in the transaction that starts its session. A resumed run keeps the old run's commit, image and
// finished steps, so an unchanged step finds its result. Failed steps stay behind and run again, and a workflow step always
// runs again so that its script is read afresh. The kept steps cost this run nothing, so they carry no usage.
func insertWorkflow(ctx context.Context, tx *sql.Tx, session contract.SessionID, run WorkflowRun, at time.Time) error {
	profiles, err := json.Marshal(run.Profiles)
	if err != nil {
		return err
	}
	if run.Resume != "" {
		err := tx.QueryRowContext(ctx, "SELECT commit_sha,image FROM workflow_runs WHERE id=?", run.Resume).Scan(&run.Commit, &run.Image)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO workflow_runs(id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,started_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", run.ID, run.Room, session, run.Name, run.Description, run.Script, run.Args, string(profiles), run.Repo, run.Commit, run.Image, contract.WorkflowRunning, at.UnixMilli())
	if err != nil || run.Resume == "" {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_steps(run,key,parent,kind,name,phase,profile,detail,state,result,transcript,started_at,ended_at)
		SELECT ?,key,parent,kind,name,phase,profile,detail,state,result,transcript,started_at,ended_at FROM workflow_steps WHERE run=? AND state=? AND kind!=?`,
		run.ID, run.Resume, contract.WorkflowStepDone, contract.WorkflowStepWorkflow)
	return err
}

// SessionWorkflow finds the run a crew session hosts. Any other session has none, which is ErrNotFound.
func (s *Store) SessionWorkflow(ctx context.Context, session contract.SessionID) (WorkflowRun, error) {
	return scanWorkflow(s.Read().QueryRowContext(ctx, "SELECT "+workflowColumns+" FROM workflow_runs WHERE session=?", session))
}

func (s *Store) WorkflowRun(ctx context.Context, id contract.WorkflowRunID) (WorkflowRun, error) {
	return scanWorkflow(s.Read().QueryRowContext(ctx, "SELECT "+workflowColumns+" FROM workflow_runs WHERE id=?", id))
}

// WorkflowRuns lists a room's newest runs.
func (s *Store) WorkflowRuns(ctx context.Context, room contract.RoomID) ([]WorkflowRun, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT "+workflowColumns+" FROM workflow_runs WHERE room=? ORDER BY started_at DESC,id DESC LIMIT 100", room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []WorkflowRun
	for rows.Next() {
		run, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// PinWorkflowCommit keeps the first commit reported.
func (s *Store) PinWorkflowCommit(ctx context.Context, id contract.WorkflowRunID, commit string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE workflow_runs SET commit_sha=? WHERE id=? AND commit_sha=''", commit, id)
		return err
	})
}

// PinWorkflowImage returns the image of the session's run, setting it to image the first time. Another session has none.
func (s *Store) PinWorkflowImage(ctx context.Context, session contract.SessionID, image string) (string, error) {
	var pinned string
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE workflow_runs SET image=? WHERE session=? AND image=''", image, session); err != nil {
			return err
		}
		err := tx.QueryRowContext(ctx, "SELECT image FROM workflow_runs WHERE session=?", session).Scan(&pinned)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return pinned, err
}

// StartWorkflowStep returns a step's stored outcome, or records it as running when it has none. A step that was running when
// its box went has no outcome, so it runs again. A workflow step always does, and its inner steps find their results.
func (s *Store) StartWorkflowStep(ctx context.Context, run contract.WorkflowRunID, req contract.WorkflowStepStartRequest, at time.Time) (contract.WorkflowStepStartResponse, error) {
	var stored contract.WorkflowStepStartResponse
	err := s.Write(ctx, func(tx *sql.Tx) error {
		stored = contract.WorkflowStepStartResponse{}
		err := tx.QueryRowContext(ctx, "SELECT state,result,reason FROM workflow_steps WHERE run=? AND key=?", run, req.Key).Scan(&stored.State, &stored.Result, &stored.Reason)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if (stored.State == contract.WorkflowStepDone || stored.State == contract.WorkflowStepFailed) && req.Kind != contract.WorkflowStepWorkflow {
			return nil
		}
		stored = contract.WorkflowStepStartResponse{}
		_, err = tx.ExecContext(ctx, `INSERT INTO workflow_steps(run,key,parent,kind,name,phase,profile,detail,state,started_at) VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(run,key) DO UPDATE SET state=excluded.state,reason='',transcript='',started_at=excluded.started_at,ended_at=0`, run, req.Key, req.Parent, req.Kind, req.Name, req.Phase, req.Profile, req.Detail, contract.WorkflowStepRunning, at.UnixMilli())
		return err
	})
	return stored, err
}

// EndWorkflowStep stores a step's outcome. A retried report writes the same row.
func (s *Store) EndWorkflowStep(ctx context.Context, run contract.WorkflowRunID, req contract.WorkflowStepEndRequest, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE workflow_steps SET state=?,result=?,reason=?,transcript=?,input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=?,ended_at=? WHERE run=? AND key=?",
			req.State, req.Result, req.Reason, req.Transcript, req.Usage.InputTokens, req.Usage.OutputTokens, req.Usage.CacheReadTokens, req.Usage.CacheWriteTokens, at.UnixMilli(), run, req.Key)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SetWorkflowStepTranscript names the file of a worker that is running. Its end names it again, so a late report changes nothing.
func (s *Store) SetWorkflowStepTranscript(ctx context.Context, run contract.WorkflowRunID, key, path string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE workflow_steps SET transcript=? WHERE run=? AND key=? AND state=?", path, run, key, contract.WorkflowStepRunning)
		return err
	})
}

const workflowStepColumns = "key,parent,kind,name,phase,profile,state,reason,transcript,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_at,ended_at"

func scanWorkflowStep(row interface{ Scan(...any) error }, extra ...any) (contract.WorkflowStep, error) {
	var step contract.WorkflowStep
	var started, ended int64
	if err := row.Scan(append([]any{&step.Key, &step.Parent, &step.Kind, &step.Name, &step.Phase, &step.Profile, &step.State, &step.Reason, &step.Transcript, &step.Usage.InputTokens, &step.Usage.OutputTokens, &step.Usage.CacheReadTokens, &step.Usage.CacheWriteTokens, &started, &ended}, extra...)...); err != nil {
		return step, err
	}
	step.Started = time.UnixMilli(started).UTC()
	if ended != 0 {
		step.Ended = time.UnixMilli(ended).UTC()
	}
	return step, nil
}

// WorkflowSteps lists a run's journal in start order.
func (s *Store) WorkflowSteps(ctx context.Context, run contract.WorkflowRunID) ([]contract.WorkflowStep, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT "+workflowStepColumns+" FROM workflow_steps WHERE run=? ORDER BY started_at,rowid", run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	steps := []contract.WorkflowStep{}
	for rows.Next() {
		step, err := scanWorkflowStep(rows)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// WorkflowStep reads one journal row.
func (s *Store) WorkflowStep(ctx context.Context, run contract.WorkflowRunID, key string) (contract.WorkflowStep, error) {
	return scanWorkflowStep(s.Read().QueryRowContext(ctx, "SELECT "+workflowStepColumns+" FROM workflow_steps WHERE run=? AND key=?", run, key))
}

// WorkerTranscript is where one worker's transcript lies. Session is the crew that ran it: a resumed run keeps a finished step's
// row, but the file stays with the run that wrote it. Turn is empty until the file's first user record reaches the engine.
type WorkerTranscript struct {
	Step    contract.WorkflowStep
	Prompt  string
	Session contract.SessionID
	Turn    contract.TurnID
}

// WorkflowWorker finds a crew's step and the transcript it names. A session with no such step is ErrNotFound.
func (s *Store) WorkflowWorker(ctx context.Context, session contract.SessionID, key string) (WorkerTranscript, error) {
	worker := WorkerTranscript{Session: session}
	var err error
	worker.Step, err = scanWorkflowStep(s.Read().QueryRowContext(ctx, "SELECT "+workflowStepColumns+",detail FROM workflow_steps WHERE key=? AND run=(SELECT id FROM workflow_runs WHERE session=?)", key, session), &worker.Prompt)
	if err != nil || worker.Step.Transcript == "" {
		return worker, err
	}
	err = s.Read().QueryRowContext(ctx, `SELECT r.session FROM workflow_runs r JOIN workflow_steps t ON t.run=r.id AND t.key=?
		WHERE r.room=(SELECT room FROM workflow_runs WHERE session=?) AND t.transcript=? ORDER BY r.started_at LIMIT 1`, key, session, worker.Step.Transcript).Scan(&worker.Session)
	if err != nil {
		return worker, err
	}
	err = s.Read().QueryRowContext(ctx, "SELECT turn FROM transcript_turns WHERE session=? AND path=? ORDER BY start DESC LIMIT 1", worker.Session, worker.Step.Transcript).Scan(&worker.Turn)
	if errors.Is(err, ErrNotFound) {
		return worker, nil
	}
	return worker, err
}

// WorkflowMerge reads the newest merge() result of a run. A merge is a run step, and only its result holds conflicts.
func (s *Store) WorkflowMerge(ctx context.Context, run contract.WorkflowRunID) (*contract.WorkflowMerge, error) {
	var data string
	err := s.Read().QueryRowContext(ctx, `SELECT result FROM workflow_steps WHERE run=? AND kind=? AND state=? AND CASE WHEN json_valid(result) THEN json_type(result,'$.conflicts') END='array' ORDER BY started_at DESC,rowid DESC LIMIT 1`, run, contract.WorkflowStepCommand, contract.WorkflowStepDone).Scan(&data)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var merge contract.WorkflowMerge
	return &merge, json.Unmarshal([]byte(data), &merge)
}

// WorkflowSummary totals a run's journal: its failed steps, and the usage of every step, which is the crew's usage.
func (s *Store) WorkflowSummary(ctx context.Context, run contract.WorkflowRunID) (contract.WorkflowEnded, error) {
	summary := contract.WorkflowEnded{Run: run}
	steps, err := s.WorkflowSteps(ctx, run)
	if err != nil {
		return summary, err
	}
	for _, step := range steps {
		summary.Usage.InputTokens += step.Usage.InputTokens
		summary.Usage.OutputTokens += step.Usage.OutputTokens
		summary.Usage.CacheReadTokens += step.Usage.CacheReadTokens
		summary.Usage.CacheWriteTokens += step.Usage.CacheWriteTokens
		if step.State == contract.WorkflowStepFailed {
			summary.Failed++
			if len(summary.FailedSteps) < failedStepNames {
				summary.FailedSteps = append(summary.FailedSteps, step.Name)
			}
		}
	}
	return summary, nil
}

// AbandonWorkflowSteps fails the steps still running as a run ends, which its script started and did not wait for.
func (s *Store) AbandonWorkflowSteps(ctx context.Context, run contract.WorkflowRunID, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE workflow_steps SET state=?,reason='the run ended first',ended_at=? WHERE run=? AND state=?", contract.WorkflowStepFailed, at.UnixMilli(), run, contract.WorkflowStepRunning)
		return err
	})
}

// EndWorkflow records a run's end and its event once.
func (s *Store) EndWorkflow(ctx context.Context, id contract.WorkflowRunID, state contract.WorkflowState, result string, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error { return s.endWorkflow(ctx, tx, id, state, result, event) })
}

// EndWorkflowTurn ends a crew's turn and its run together, so no reply goes out for a run still marked running.
func (s *Store) EndWorkflowTurn(ctx context.Context, session contract.SessionID, output contract.TurnOutputRequest, turn contract.Event, id contract.WorkflowRunID, state contract.WorkflowState, result string, event contract.Event) error {
	return s.EndTurn(ctx, session, output, turn, func(tx *sql.Tx) error { return s.endWorkflow(ctx, tx, id, state, result, event) })
}

func (s *Store) endWorkflow(ctx context.Context, tx *sql.Tx, id contract.WorkflowRunID, state contract.WorkflowState, result string, event contract.Event) error {
	ended, err := tx.ExecContext(ctx, "UPDATE workflow_runs SET state=?,result=?,ended_at=? WHERE id=? AND state=?", state, result, event.Time.UnixMilli(), id, contract.WorkflowRunning)
	if err != nil {
		return err
	}
	if n, err := ended.RowsAffected(); err != nil || n == 0 {
		return err
	}
	_, err = s.AppendEvent(tx, event)
	return err
}
