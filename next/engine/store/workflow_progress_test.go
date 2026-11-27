package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestWorkflowProgress(t *testing.T) {
	s, ctx := branchStore(t)
	for _, q := range []string{
		`INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('crew','room','crew','p','ready','',0),('resumed','room','resumed','p','ready','',0)`,
		`INSERT INTO workflow_runs(id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,started_at) VALUES('run','room','crew','test','','script','','[]','owner/repo','','','running',1)`,
	} {
		if _, err := s.writer.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StartWorkflowStep(ctx, "run", contract.WorkflowStepStartRequest{Key: "step", Kind: contract.WorkflowStepAgent, Name: "agent test"}, time.UnixMilli(10)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkflowStepTranscript(ctx, "run", "step", "worker.jsonl"); err != nil {
		t.Fatal(err)
	}
	initial, err := s.WorkflowProgress(ctx, "crew", "worker.jsonl")
	if err != nil || initial != (WorkflowStepProgress{}) {
		t.Fatalf("initial %+v: %v", initial, err)
	}
	for _, lookup := range [][2]string{{"missing", "worker.jsonl"}, {"crew", "missing"}} {
		if _, err := s.WorkflowProgress(ctx, contract.SessionID(lookup[0]), lookup[1]); !errors.Is(err, ErrNotFound) {
			t.Fatalf("lookup: %v", err)
		}
	}
	progress := WorkflowStepProgress{Offset: 100, ToolCalls: 3, Usage: contract.Usage{InputTokens: 11, OutputTokens: 12, CacheReadTokens: 13, CacheWriteTokens: 14}, LastActivity: time.UnixMilli(30).UTC()}
	if err := s.SetWorkflowProgress(ctx, "crew", "worker.jsonl", 0, progress); err != nil {
		t.Fatal(err)
	}
	if got, err := s.WorkflowProgress(ctx, "crew", "worker.jsonl"); err != nil || got != progress {
		t.Fatalf("progress %+v: %v", got, err)
	}
	changed := progress
	changed.Offset = 200
	changed.ToolCalls = 9
	if err := s.SetWorkflowProgress(ctx, "crew", "worker.jsonl", 0, changed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale CAS: %v", err)
	}
	changed.Offset = 0
	if err := s.SetWorkflowProgress(ctx, "crew", "worker.jsonl", 100, changed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("offset regression: %v", err)
	}
	// A reconnect naming the same file must retain all complete records already counted.
	if err := s.SetWorkflowStepTranscript(ctx, "run", "step", "worker.jsonl"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.WorkflowProgress(ctx, "crew", "worker.jsonl"); err != nil || got != progress {
		t.Fatalf("reconnect %+v: %v", got, err)
	}
	final := contract.Usage{InputTokens: 21, OutputTokens: 22, CacheReadTokens: 23, CacheWriteTokens: 24}
	if err := s.EndWorkflowStep(ctx, "run", contract.WorkflowStepEndRequest{Key: "step", State: contract.WorkflowStepDone, Transcript: "worker.jsonl", Usage: final}, time.UnixMilli(40)); err != nil {
		t.Fatal(err)
	}
	changed.Offset = 200
	if err := s.SetWorkflowProgress(ctx, "crew", "worker.jsonl", 100, changed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finished update: %v", err)
	}
	if _, err := s.WorkflowProgress(ctx, "crew", "worker.jsonl"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finished lookup: %v", err)
	}
	step, err := s.WorkflowStep(ctx, "run", "step")
	if err != nil || step.ToolCalls != 3 || step.Usage != final || !step.LastActivity.Equal(progress.LastActivity) {
		t.Fatalf("frozen %+v: %v", step, err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		return insertWorkflow(ctx, tx, "resumed", WorkflowRun{ID: "resume", Room: "room", Name: "test", Resume: "run", Repo: "owner/repo"}, time.UnixMilli(50))
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := s.WorkflowStep(ctx, "resume", "step")
	// Kept steps retain activity and counts but do not charge tokens to the new run.
	step.Usage = contract.Usage{}
	if err != nil || resumed != step {
		t.Fatalf("resumed %+v; original %+v: %v", resumed, step, err)
	}
	var offset int64
	if err := s.Read().QueryRowContext(ctx, "SELECT progress_offset FROM workflow_steps WHERE run='resume' AND key='step'").Scan(&offset); err != nil || offset != progress.Offset {
		t.Fatalf("resumed offset %d: %v", offset, err)
	}
	summary, err := s.WorkflowSummary(ctx, "resume")
	if err != nil || summary.Usage != (contract.Usage{}) {
		t.Fatalf("summary %+v: %v", summary, err)
	}
}

func TestWorkflowProgressRestart(t *testing.T) {
	s, ctx := branchStore(t)
	for _, q := range []string{
		`INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('crew','room','crew','p','ready','',0)`,
		`INSERT INTO workflow_runs(id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,started_at) VALUES('run','room','crew','test','','script','','[]','owner/repo','','','running',1)`,
	} {
		if _, err := s.writer.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	req := contract.WorkflowStepStartRequest{Key: "step", Kind: contract.WorkflowStepAgent, Name: "agent test"}
	if _, err := s.StartWorkflowStep(ctx, "run", req, time.UnixMilli(10)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkflowStepTranscript(ctx, "run", "step", "first.jsonl"); err != nil {
		t.Fatal(err)
	}
	progress := WorkflowStepProgress{Offset: 100, ToolCalls: 3, Usage: contract.Usage{InputTokens: 11, OutputTokens: 12, CacheReadTokens: 13, CacheWriteTokens: 14}, LastActivity: time.UnixMilli(30).UTC()}
	if err := s.SetWorkflowProgress(ctx, "crew", "first.jsonl", 0, progress); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartWorkflowStep(ctx, "run", req, time.UnixMilli(40)); err != nil {
		t.Fatal(err)
	}
	step, err := s.WorkflowStep(ctx, "run", "step")
	if err != nil || step.Transcript != "" || step.ToolCalls != 0 || !step.LastActivity.IsZero() || step.Usage != (contract.Usage{}) || !step.Started.Equal(time.UnixMilli(40)) {
		t.Fatalf("restarted step %+v: %v", step, err)
	}
	if err := s.SetWorkflowProgress(ctx, "crew", "first.jsonl", 100, progress); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old worker update: %v", err)
	}
	if err := s.SetWorkflowStepTranscript(ctx, "run", "step", "second.jsonl"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.WorkflowProgress(ctx, "crew", "second.jsonl"); err != nil || got != (WorkflowStepProgress{}) {
		t.Fatalf("new worker progress %+v: %v", got, err)
	}
	if err := s.SetWorkflowProgress(ctx, "crew", "second.jsonl", 0, progress); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowProgressMigration(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('room','room',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('crew','room','crew','p','ready','',0);
 INSERT INTO workflow_runs(id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,started_at) VALUES('run','room','crew','test','','script','','[]','owner/repo','','','running',1);
 INSERT INTO workflow_steps(run,key,parent,kind,name,phase,profile,detail,state,input_tokens,started_at) VALUES('run','step','','agent','test','','p','','running',7,1);
 DROP INDEX workflow_runs_service; DROP TABLE workflow_streaks; ALTER TABLE workflow_runs DROP COLUMN served; DROP INDEX workflow_queue_key; ALTER TABLE workflow_queue DROP COLUMN boot; ALTER TABLE workflow_queue DROP COLUMN handovers; ALTER TABLE workflow_steps DROP COLUMN event; ALTER TABLE workflow_steps DROP COLUMN progress_offset;
 ALTER TABLE workflow_steps DROP COLUMN tool_calls;
 ALTER TABLE workflow_steps DROP COLUMN last_activity;
 ALTER TABLE room_repos DROP COLUMN ci_hosts; ALTER TABLE workflow_steps DROP COLUMN raw_text; DROP TABLE browsers; ALTER TABLE rooms DROP COLUMN browser; DROP TABLE service_feed_messages; DROP TABLE service_outbox; ALTER TABLE services DROP COLUMN feed_in; ALTER TABLE services DROP COLUMN feed_out; ALTER TABLE services DROP COLUMN feed_last_at; ALTER TABLE turns DROP COLUMN harness; ALTER TABLE boxes DROP COLUMN harness; ALTER TABLE boxes DROP COLUMN can_handoff; ALTER TABLE room_repos DROP COLUMN step_branch_expiry; ALTER TABLE boxes DROP COLUMN agy; ALTER TABLE hosts DROP COLUMN agy; ALTER TABLE boxes DROP COLUMN launch_gen; ALTER TABLE sessions DROP COLUMN handoff; ALTER TABLE sessions DROP COLUMN cleared_at; ALTER TABLE turns DROP COLUMN cleared_at; UPDATE meta SET value='122' WHERE key='schema_version';`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var offset, calls, activity, input int64
	if err := s.Read().QueryRow("SELECT progress_offset,tool_calls,last_activity,input_tokens FROM workflow_steps").Scan(&offset, &calls, &activity, &input); err != nil || offset != 0 || calls != 0 || activity != 0 || input != 7 {
		t.Fatalf("migration %d %d %d %d: %v", offset, calls, activity, input, err)
	}
}
