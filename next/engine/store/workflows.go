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

// WorkflowRun is one run of a script, tied to the crew session that hosts it. Repo never changes. Script is the newest version,
// and Description and Profiles are its meta's: a reload replaces all three and adds one to Version. Commit and Image are set
// once, by the box's first start. Resume names the run this one resumes, and only a run's start reads it from here: afterwards
// ResumedSessions follows the stored link.
type WorkflowRun struct {
	ID           contract.WorkflowRunID
	Room         contract.RoomID
	Session      contract.SessionID
	Subscription contract.SubscriptionID
	Key          string
	Queue        int64 // pending subscription event, bound atomically at session creation
	Name         string
	Description  string
	Script       string
	Args         string
	Profiles     []contract.ProfileName
	Repo         contract.RepoName
	Commit       string
	Image        string
	State        contract.WorkflowState
	PausedSteps  []string
	Paused       bool
	Result       string
	Resume       contract.WorkflowRunID
	MaxRuns      int // the most runs going in the room, counted when this one starts; 0 is no limit
	Version      int
	StatusLines  []contract.WorkflowStatusLine // the script's latest status() lines, oldest first
	Started      time.Time
	Ended        time.Time
}

// workflowStatusLines is how many status() lines a run keeps.
const workflowStatusLines = 20

const workflowColumns = "sub,key,id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,result,version,status_lines,started_at,ended_at,paused,paused_steps"

func scanWorkflow(row interface{ Scan(...any) error }) (WorkflowRun, error) {
	var run WorkflowRun
	var profiles, lines, pausedSteps string
	var started, ended int64
	err := row.Scan(&run.Subscription, &run.Key, &run.ID, &run.Room, &run.Session, &run.Name, &run.Description, &run.Script, &run.Args, &profiles, &run.Repo, &run.Commit, &run.Image, &run.State, &run.Result, &run.Version, &lines, &started, &ended, &run.Paused, &pausedSteps)
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(pausedSteps), &run.PausedSteps); err != nil {
		return run, err
	}
	run.Started = time.UnixMilli(started).UTC()
	run.Ended = fromMillis(ended)
	if err := json.Unmarshal([]byte(lines), &run.StatusLines); err != nil {
		return run, err
	}
	return run, json.Unmarshal([]byte(profiles), &run.Profiles)
}

// insertWorkflow adds a run in the transaction that starts its session. A resumed run keeps the old run's commit, image and
// finished steps, so an unchanged step finds its result. Failed steps stay behind and run again, and a workflow step always
// runs again so that its script is read afresh. The kept steps cost this run nothing, so they carry no usage.
func insertWorkflow(ctx context.Context, tx *sql.Tx, session contract.SessionID, run WorkflowRun, at time.Time) error {
	if run.MaxRuns > 0 {
		var going int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM workflow_runs WHERE room=? AND state=?", run.Room, contract.WorkflowRunning).Scan(&going); err != nil {
			return err
		}
		if going >= run.MaxRuns {
			return ErrWorkflowRunLimit
		}
	}
	if run.Queue != 0 {
		if err := tx.QueryRowContext(ctx, "SELECT sub,key FROM workflow_queue WHERE id=?", run.Queue).Scan(&run.Subscription, &run.Key); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE workflow_queue SET run=? WHERE id=? AND run='starting'", run.ID, run.Queue)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "UPDATE subscriptions SET failures=0,hold_until=0 WHERE id=?", run.Subscription); err != nil {
			return err
		}
	}
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
	_, err = tx.ExecContext(ctx, "INSERT INTO workflow_runs(sub,key,id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,started_at,resumes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", run.Subscription, run.Key, run.ID, run.Room, session, run.Name, run.Description, run.Script, run.Args, string(profiles), run.Repo, run.Commit, run.Image, contract.WorkflowRunning, at.UnixMilli(), run.Resume)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO workflow_scripts(run,version,script,created_at) VALUES(?,1,?,?)", run.ID, run.Script, at.UnixMilli())
	if err != nil || run.Resume == "" {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_steps(run,key,parent,kind,name,phase,lane,profile,detail,state,result,transcript,started_at,ended_at)
		SELECT ?,key,parent,kind,name,phase,lane,profile,detail,state,result,transcript,started_at,ended_at FROM workflow_steps WHERE run=? AND state=? AND kind!=?`,
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
	return queryAll(ctx, s.Read(), scanWorkflow, "SELECT "+workflowColumns+" FROM workflow_runs WHERE room=? ORDER BY started_at DESC,id DESC LIMIT 100", room)
}

// WorkflowList reads lightweight headers and journal totals in one statement, without scripts or results.
// Branch is the crew's first recorded branch, with rowid breaking timestamp ties.
func (s *Store) WorkflowList(ctx context.Context, room contract.RoomID) ([]contract.WorkflowInfo, error) {
	rows, err := s.Read().QueryContext(ctx, `WITH runs AS (
 SELECT sub,key,id,room,session,name,description,profiles,repo,commit_sha,version,state,status_lines,started_at,ended_at
 FROM workflow_runs WHERE room=? ORDER BY started_at DESC,id DESC LIMIT 100
 ), steps AS (
 SELECT w.run,w.state,w.name,w.started_at,w.input_tokens,w.output_tokens,w.cache_read_tokens,w.cache_write_tokens, row_number() OVER (PARTITION BY w.run,w.state ORDER BY w.started_at,w.rowid) AS ordinal
 FROM workflow_steps w JOIN runs r ON r.id=w.run
 ), totals AS (
 SELECT run,COUNT(*) AS steps,SUM(state='running') AS running,SUM(state='failed') AS failed,
 json_group_array(name ORDER BY started_at,ordinal) FILTER (WHERE state='failed' AND ordinal<=20) AS names,
 SUM(input_tokens) AS input,SUM(output_tokens) AS output,SUM(cache_read_tokens) AS cache_read,SUM(cache_write_tokens) AS cache_write
 FROM steps GROUP BY run
 ) SELECT r.sub,r.key,r.id,r.room,r.session,r.name,r.description,r.profiles,r.repo,r.commit_sha,r.version,r.state,r.status_lines,r.started_at,r.ended_at,
 COALESCE((SELECT branch FROM session_branches b WHERE b.repo=r.repo AND b.session=r.session ORDER BY created_at,rowid LIMIT 1),''),
 COALESCE(t.steps,0),COALESCE(t.running,0),COALESCE(t.failed,0),COALESCE(t.names,'[]'),
 COALESCE(t.input,0),COALESCE(t.output,0),COALESCE(t.cache_read,0),COALESCE(t.cache_write,0)
 FROM runs r LEFT JOIN totals t ON t.run=r.id ORDER BY r.started_at DESC,r.id DESC`, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []contract.WorkflowInfo{}
	for rows.Next() {
		var run contract.WorkflowInfo
		var profiles, lines, names string
		var started, ended int64
		if err := rows.Scan(&run.Subscription, &run.Key, &run.Run, &run.Room, &run.Session, &run.Name, &run.Description, &profiles, &run.Repo, &run.Commit, &run.Version, &run.State, &lines, &started, &ended, &run.Branch, &run.Steps, &run.Running, &run.Failed, &names, &run.Usage.InputTokens, &run.Usage.OutputTokens, &run.Usage.CacheReadTokens, &run.Usage.CacheWriteTokens); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(profiles), &run.Profiles); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(names), &run.FailedSteps); err != nil {
			return nil, err
		}
		var status []contract.WorkflowStatusLine
		if err := json.Unmarshal([]byte(lines), &status); err != nil {
			return nil, err
		}
		if len(status) > 0 {
			run.Status, run.StatusAt = status[len(status)-1].Text, status[len(status)-1].At
		}
		run.Started = time.UnixMilli(started).UTC()
		run.Ended = fromMillis(ended)
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// ResumedSessions lists the crews of the runs that session's run resumes, through any chain of resumes, in its room and on repo.
// Any other session has none. A link that leaves the room or the repository ends the chain.
func (s *Store) ResumedSessions(ctx context.Context, repo contract.RepoName, session contract.SessionID) ([]contract.SessionID, error) {
	return listIDs[contract.SessionID](ctx, s.Read(), `WITH RECURSIVE chain(session,room,resumes) AS (
		SELECT session,room,resumes FROM workflow_runs WHERE session=? AND repo=?
		UNION
		SELECT r.session,r.room,r.resumes FROM workflow_runs r JOIN chain c ON r.id=c.resumes AND r.room=c.room WHERE r.repo=?)
		SELECT session FROM chain WHERE session!=?`, session, repo, repo, session)
}

// ErrWorkflowEnded refuses a control or post to a run that is no longer running.
var ErrWorkflowEnded = errors.New("the workflow run has ended")

// ReloadWorkflow makes script the run's newest version, with its meta's description and profiles, keeps it beside the earlier
// ones, and wakes the crew's channel to carry it to the box. It returns the new version.
func (s *Store) ReloadWorkflow(ctx context.Context, id contract.WorkflowRunID, script, description string, profiles []contract.ProfileName, at time.Time) (int, error) {
	listed, err := json.Marshal(profiles)
	if err != nil {
		return 0, err
	}
	var version int
	err = s.Write(ctx, func(tx *sql.Tx) error {
		var session contract.SessionID
		err := tx.QueryRowContext(ctx, "UPDATE workflow_runs SET script=?,description=?,profiles=?,version=version+1 WHERE id=? AND state=? RETURNING version,session", script, description, string(listed), id, contract.WorkflowRunning).Scan(&version, &session)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrWorkflowEnded
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO workflow_scripts(run,version,script,created_at) VALUES(?,?,?,?)", id, version, script, at.UnixMilli()); err != nil {
			return err
		}
		s.wakePending = append(s.wakePending, session)
		return nil
	})
	return version, err
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

// StartWorkflowStep returns a step's stored outcome, or records it as running when it has none, under the script version that
// asked. A step that was running when its box went has no outcome, so it runs again. A workflow step always does, and its inner
// steps find their results. A lane is not part of a key, so a stored step that had none takes the one its script now gives it.
func (s *Store) StartWorkflowStep(ctx context.Context, run contract.WorkflowRunID, req contract.WorkflowStepStartRequest, at time.Time) (contract.WorkflowStepStartResponse, error) {
	var stored contract.WorkflowStepStartResponse
	err := s.Write(ctx, func(tx *sql.Tx) error {
		stored = contract.WorkflowStepStartResponse{}
		err := tx.QueryRowContext(ctx, "SELECT state,result,reason FROM workflow_steps WHERE run=? AND key=?", run, req.Key).Scan(&stored.State, &stored.Result, &stored.Reason)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if (stored.State == contract.WorkflowStepDone || stored.State == contract.WorkflowStepFailed) && req.Kind != contract.WorkflowStepWorkflow {
			if req.Lane == "" {
				return nil
			}
			_, err := tx.ExecContext(ctx, "UPDATE workflow_steps SET lane=? WHERE run=? AND key=? AND lane=''", req.Lane, run, req.Key)
			return err
		}
		stored = contract.WorkflowStepStartResponse{}
		_, err = tx.ExecContext(ctx, `INSERT INTO workflow_steps(run,key,parent,kind,name,phase,lane,profile,detail,state,started_at,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(run,key) DO UPDATE SET state=excluded.state,lane=excluded.lane,reason='',transcript='',started_at=excluded.started_at,ended_at=0,version=excluded.version`, run, req.Key, req.Parent, req.Kind, req.Name, req.Phase, req.Lane, req.Profile, req.Detail, contract.WorkflowStepRunning, at.UnixMilli(), req.Version)
		return err
	})
	return stored, err
}

// SetWorkflowStatus adds a status() line to a run, which keeps the latest workflowStatusLines of them.
func (s *Store) SetWorkflowStatus(ctx context.Context, run contract.WorkflowRunID, text string, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var data string
		if err := tx.QueryRowContext(ctx, "SELECT status_lines FROM workflow_runs WHERE id=?", run).Scan(&data); err != nil {
			return err
		}
		var lines []contract.WorkflowStatusLine
		if err := json.Unmarshal([]byte(data), &lines); err != nil {
			return err
		}
		lines = append(lines, contract.WorkflowStatusLine{Text: text, At: at.UTC()})
		if extra := len(lines) - workflowStatusLines; extra > 0 {
			lines = lines[extra:]
		}
		kept, err := json.Marshal(lines)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE workflow_runs SET status_lines=? WHERE id=?", string(kept), run)
		return err
	})
}

// WorkflowCounts counts a run's steps, its running steps and its failed steps.
func (s *Store) WorkflowCounts(ctx context.Context, run contract.WorkflowRunID) (steps, running, failed int, err error) {
	err = s.Read().QueryRowContext(ctx, "SELECT COUNT(*),COUNT(CASE WHEN state=? THEN 1 END),COUNT(CASE WHEN state=? THEN 1 END) FROM workflow_steps WHERE run=?",
		contract.WorkflowStepRunning, contract.WorkflowStepFailed, run).Scan(&steps, &running, &failed)
	return steps, running, failed, err
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

// workflowStepMore says a step holds more than its row: what it ran beyond the words its name quotes, or what a command wrote.
const workflowStepMore = `ifnull(trim(detail,char(32,9,10,13)) NOT IN ('',substr(name,length(kind)+3)) OR CASE WHEN kind='run' AND json_valid(result) THEN ifnull(json_extract(result,'$.stdout'),'')!='' OR ifnull(json_extract(result,'$.stderr'),'')!='' OR ifnull(json_extract(result,'$.code'),0)!=0 OR json_type(result,'$.conflicts')='array' ELSE 0 END,0)`

const workflowStepColumns = "key,parent,kind,name,phase,lane,profile,version,state,reason,transcript," + workflowStepMore + ",input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_at,ended_at"

func scanWorkflowStep(row interface{ Scan(...any) error }, extra ...any) (contract.WorkflowStep, error) {
	var step contract.WorkflowStep
	var started, ended int64
	if err := row.Scan(append([]any{&step.Key, &step.Parent, &step.Kind, &step.Name, &step.Phase, &step.Lane, &step.Profile, &step.Version, &step.State, &step.Reason, &step.Transcript, &step.More, &step.Usage.InputTokens, &step.Usage.OutputTokens, &step.Usage.CacheReadTokens, &step.Usage.CacheWriteTokens, &started, &ended}, extra...)...); err != nil {
		return step, err
	}
	step.Started = time.UnixMilli(started).UTC()
	step.Ended = fromMillis(ended)
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

// WorkflowStepOutput reads the prompt, command or text a row ran, and its stored result.
func (s *Store) WorkflowStepOutput(ctx context.Context, run contract.WorkflowRunID, key string) (detail, result string, err error) {
	err = s.Read().QueryRowContext(ctx, "SELECT detail,result FROM workflow_steps WHERE run=? AND key=?", run, key).Scan(&detail, &result)
	return detail, result, err
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
	summary.Steps = len(steps)
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

func (s *Store) PauseWorkflow(ctx context.Context, id contract.WorkflowRunID, paused bool, steps ...string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var session contract.SessionID
		query := "UPDATE workflow_runs SET paused=? WHERE id=? AND state=? RETURNING session"
		args := []any{paused, id, contract.WorkflowRunning}
		if len(steps) > 0 && steps[0] != "" {
			query = "UPDATE workflow_runs SET paused_steps=(SELECT json_group_array(value) FROM json_each(paused_steps) WHERE value!=?) WHERE id=? AND state=? RETURNING session"
			args[0] = steps[0]
			if paused {
				query = "UPDATE workflow_runs SET paused_steps=json_insert((SELECT json_group_array(value) FROM json_each(paused_steps) WHERE value!=?), '$[#]', ?) WHERE id=? AND state=? RETURNING session"
				args = []any{steps[0], steps[0], id, contract.WorkflowRunning}
			}
		}
		err := tx.QueryRowContext(ctx, query, args...).Scan(&session)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrWorkflowEnded
		}
		if err != nil {
			return err
		}
		s.wakePending = append(s.wakePending, session)
		return nil
	})
}

// EndedWorkflowNamed recognizes a finished run's session name after SessionByName no longer lists it.
func (s *Store) EndedWorkflowNamed(ctx context.Context, room contract.RoomID, name string) (WorkflowRun, error) {
	var id contract.WorkflowRunID
	err := s.Read().QueryRowContext(ctx, "SELECT w.id FROM workflow_runs w JOIN sessions s ON s.id=w.session WHERE s.room=? AND s.name=? AND w.state!=?", room, name, contract.WorkflowRunning).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowRun{}, nil
	}
	if err != nil {
		return WorkflowRun{}, err
	}
	return s.WorkflowRun(ctx, id)
}

// WorkflowRunNamed resolves a run's session name within a room, or uniquely across rooms for the owner.
func (s *Store) WorkflowRunNamed(ctx context.Context, room contract.RoomID, name string) (WorkflowRun, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT w.id FROM workflow_runs w JOIN sessions s ON s.id=w.session WHERE s.name=? AND (?='' OR s.room=?)", name, room, room)
	if err != nil {
		return WorkflowRun{}, err
	}
	var ids []contract.WorkflowRunID
	for rows.Next() {
		var id contract.WorkflowRunID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return WorkflowRun{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return WorkflowRun{}, err
	}
	if len(ids) == 0 {
		return WorkflowRun{}, ErrNotFound
	}
	if len(ids) != 1 {
		return WorkflowRun{}, errors.New("run session name is ambiguous; use the run ID")
	}
	return s.WorkflowRun(ctx, ids[0])
}
