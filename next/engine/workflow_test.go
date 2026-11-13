package engine

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const workflowScript = "export const meta = { name: 'sweep', profiles: ['fast', 'deep'] }\nreturn await agent('look')\n"

type workflowCase struct {
	*Service
	t     *testing.T
	ctx   context.Context
	room  contract.RoomID
	root  contract.SessionID
	owner contract.Principal
}

func setupWorkflow(t *testing.T) workflowCase {
	t.Helper()
	s, room, _, ctx := setup(t)
	s.profiles = crewProfiles{}
	var root contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT id FROM sessions WHERE room=? AND parent IS NULL", room).Scan(&root); err != nil {
		t.Fatal(err)
	}
	return workflowCase{s, t, ctx, room, root, contract.Principal{Kind: contract.PrincipalOwner}}
}

func (w workflowCase) start(caller contract.Principal, script string) (contract.WorkflowRunResponse, contract.Principal) {
	w.t.Helper()
	started, problem := w.WorkflowRun(w.ctx, caller, contract.WorkflowRunRequest{Room: w.room, Script: script})
	if problem != nil {
		w.t.Fatal(problem)
	}
	return started, contract.Principal{Kind: contract.PrincipalSession, Session: started.Session}
}

// turn connects the crew's box and returns the turn its one input starts.
func (w workflowCase) turn(crew contract.Principal) contract.TurnStart {
	w.t.Helper()
	channel, problem := testChannel(w.t, w.Service, w.ctx, crew)
	if problem != nil {
		w.t.Fatal(problem)
	}
	for {
		select {
		case message := <-channel:
			if start, ok := message.Body.(contract.TurnStart); ok {
				return start
			}
		case <-time.After(10 * time.Second):
			w.t.Fatal("no turn reached the crew")
		}
	}
}

func (w workflowCase) step(crew contract.Principal, key string, state contract.WorkflowStepState, reason string) {
	w.t.Helper()
	if _, problem := w.WorkflowStepStart(w.ctx, crew, contract.WorkflowStepStartRequest{Key: key, Kind: contract.WorkflowStepAgent, Name: key, Detail: "work"}); problem != nil {
		w.t.Fatal(problem)
	}
	if state == contract.WorkflowStepRunning {
		return
	}
	end := contract.WorkflowStepEndRequest{Key: key, State: state, Reason: reason, Result: `"ok"`, Usage: contract.Usage{InputTokens: 7, OutputTokens: 3}}
	if _, problem := w.WorkflowStepEnd(w.ctx, crew, end); problem != nil {
		w.t.Fatal(problem)
	}
}

func TestACrewReachesEveryModelOfItsProfilesAndNoOther(t *testing.T) {
	w := setupWorkflow(t)
	script := "export const meta = { name: 'one', profiles: ['deep'] }"
	started, _ := w.start(w.owner, workflowScript)
	only, _ := w.start(w.owner, script)
	for _, c := range []struct {
		session contract.SessionID
		allowed bool
	}{{started.Session, true}, {only.Session, false}} {
		session, err := w.store.Session(w.ctx, c.session)
		if err != nil {
			t.Fatal(err)
		}
		models, crew, err := w.AllowedModels(w.ctx, session)
		if err != nil {
			t.Fatal(err)
		}
		if !crew || !slices.Contains(models, crewDeepModel) || slices.Contains(models, crewFastModel) != c.allowed {
			t.Fatalf("session %s: %v, crew %v", c.session, models, crew)
		}
	}
	root, err := w.store.Session(w.ctx, w.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, crew, err := w.AllowedModels(w.ctx, root); err == nil && crew {
		t.Fatal("root is not a crew")
	}
}

func TestAClosedCrewKeepsWhatItsStepsRanAndCountsThem(t *testing.T) {
	w := setupWorkflow(t)
	started, crew := w.start(w.owner, workflowScript)
	w.turn(crew)
	if _, problem := w.WorkflowStepStart(w.ctx, crew, contract.WorkflowStepStartRequest{Key: "cmd", Kind: contract.WorkflowStepCommand, Name: "run: deploy", Detail: "deploy --token=hunter2hunter2"}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := w.WorkflowStepEnd(w.ctx, crew, contract.WorkflowStepEndRequest{Key: "cmd", State: contract.WorkflowStepDone, Result: `{"code":0,"stdout":"token=hunter2hunter2 sent"}`}); problem != nil {
		t.Fatal(problem)
	}
	w.step(crew, "other", contract.WorkflowStepFailed, "refused")
	// A merge conflict joins the journal as a failed log, and every count of the run takes it in.
	if _, problem := w.WorkflowStepStart(w.ctx, crew, contract.WorkflowStepStartRequest{Key: "conflict", Kind: contract.WorkflowStepLog, Name: "merge conflict: fix-1"}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := w.WorkflowStepEnd(w.ctx, crew, contract.WorkflowStepEndRequest{Key: "conflict", State: contract.WorkflowStepFailed, Reason: "conflicts in a.go"}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := w.SessionClose(w.ctx, w.owner, contract.SessionCloseRequest{Session: started.Session}); problem != nil {
		t.Fatal(problem)
	}
	output, problem := w.WorkflowStep(w.ctx, w.owner, contract.WorkflowStepRequest{Session: started.Session, Key: "cmd"})
	if problem != nil {
		t.Fatal(problem)
	}
	if strings.Contains(output.Detail, "hunter2") || strings.Contains(output.Result, "hunter2") || !strings.HasPrefix(output.Detail, "deploy") || !strings.Contains(output.Result, `"code":0`) {
		t.Fatalf("output %+v", output)
	}
	if _, problem := w.WorkflowStep(w.ctx, w.owner, contract.WorkflowStepRequest{Session: started.Session, Key: "nothing"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("a step that is not in the journal: %+v", problem)
	}
	if _, problem := w.WorkflowStep(w.ctx, w.owner, contract.WorkflowStepRequest{Session: w.root, Key: "cmd"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("a session with no run: %+v", problem)
	}
	// The page counts the journal, Past runs reads the list and the feed reads the event. All three say the same.
	status, problem := w.WorkflowStatus(w.ctx, w.owner, contract.WorkflowStatusRequest{Run: started.Run})
	if problem != nil {
		t.Fatal(problem)
	}
	failed := 0
	for _, step := range status.Steps {
		if step.State == contract.WorkflowStepFailed {
			failed++
		}
	}
	if len(status.Steps) != 3 || failed != 2 || status.Run.Steps != 3 || status.Run.Failed != 2 {
		t.Fatalf("journal %d steps, %d failed; run %+v", len(status.Steps), failed, status.Run)
	}
	runs, problem := w.WorkflowList(w.ctx, w.owner, contract.WorkflowListRequest{Room: w.room})
	if problem != nil || len(runs.Runs) != 1 || runs.Runs[0].Steps != 3 || runs.Runs[0].Failed != 2 {
		t.Fatalf("list %+v, %+v", runs.Runs, problem)
	}
	listed, problem := w.EventsList(w.ctx, w.owner, contract.EventsListRequest{Room: w.room, Types: []contract.EventType{"workflow.ended"}, Limit: 10})
	if problem != nil || len(listed.Events) != 1 {
		t.Fatalf("%+v, %+v", listed, problem)
	}
	if ended, ok := listed.Events[0].Body.(contract.WorkflowEnded); !ok || ended.Steps != 3 || ended.Failed != 2 {
		t.Fatalf("event %+v", listed.Events[0].Body)
	}
}

func TestAStepSaysWhetherItHoldsMoreThanItsRow(t *testing.T) {
	w := setupWorkflow(t)
	_, crew := w.start(w.owner, workflowScript)
	w.turn(crew)
	long := strings.Repeat("x", 100)
	for _, c := range []struct {
		key    string
		kind   contract.WorkflowStepKind
		name   string
		detail string
		result string
		more   bool
	}{
		{"one-line", contract.WorkflowStepLog, "log: 12 issues", "12 issues", "null", false},
		{"no-text", contract.WorkflowStepLog, "merge conflict: x", "", "", false},
		{"two-lines", contract.WorkflowStepLog, "log: first", "first\nsecond", "null", true},
		{"clipped", contract.WorkflowStepLog, "log: " + long[:80], long, "null", true},
		{"post", contract.WorkflowStepPost, "post: hello", "hello", `{"seq":9}`, false},
		{"post-data", contract.WorkflowStepPost, "post: hello", "hello", `{"seq":9}`, false},
		{"quiet", contract.WorkflowStepCommand, "run: npm ci", "npm ci", `{"code":0,"stdout":"","stderr":"","durationMs":4}`, false},
		{"output", contract.WorkflowStepCommand, "run: npm ci", "npm ci", `{"code":0,"stdout":"ok","stderr":"","durationMs":4}`, true},
		{"errors", contract.WorkflowStepCommand, "run: npm ci", "npm ci", `{"code":0,"stdout":"","stderr":"warn","durationMs":4}`, true},
		{"exit", contract.WorkflowStepCommand, "run: npm ci", "npm ci", `{"code":3,"stdout":"","stderr":"","durationMs":4}`, true},
		{"merge", contract.WorkflowStepCommand, "run: merge a", "merge a", `{"branch":"b","commit":"c","merged":["a"],"conflicts":[]}`, true},
		{"labelled", contract.WorkflowStepCommand, "Install", "npm ci", `{"code":0,"stdout":"","stderr":""}`, true},
		{"cut-off", contract.WorkflowStepCommand, "run: npm ci", "npm ci", "", false},
	} {
		if _, problem := w.WorkflowStepStart(w.ctx, crew, contract.WorkflowStepStartRequest{Key: c.key, Kind: c.kind, Name: c.name, Detail: c.detail}); problem != nil {
			t.Fatal(problem)
		}
		if _, problem := w.WorkflowStepEnd(w.ctx, crew, contract.WorkflowStepEndRequest{Key: c.key, State: contract.WorkflowStepDone, Result: c.result}); problem != nil {
			t.Fatal(problem)
		}
		step, err := w.store.WorkflowStep(w.ctx, mustRun(t, w, crew), c.key)
		if err != nil {
			t.Fatal(err)
		}
		if step.More != c.more {
			t.Errorf("%s: more is %v", c.key, step.More)
		}
	}
}

func mustRun(t *testing.T, w workflowCase, crew contract.Principal) contract.WorkflowRunID {
	t.Helper()
	run, err := w.store.SessionWorkflow(w.ctx, crew.Session)
	if err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func TestAResumedRunKeepsFinishedStepsAndTheFixedCommit(t *testing.T) {
	w := setupWorkflow(t)
	started, crew := w.start(w.owner, workflowScript)
	start := w.turn(crew)
	if _, problem := w.WorkflowPin(w.ctx, crew, contract.WorkflowPinRequest{Commit: "abc"}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := w.WorkflowPin(w.ctx, crew, contract.WorkflowPinRequest{Commit: "moved"}); problem != nil {
		t.Fatal(problem)
	}
	if _, err := w.store.PinWorkflowImage(w.ctx, started.Session, "image-a"); err != nil {
		t.Fatal(err)
	}
	if pinned, err := w.store.PinWorkflowImage(w.ctx, started.Session, "image-b"); err != nil || pinned != "image-a" {
		t.Fatalf("image %q, %v", pinned, err)
	}
	if pinned, err := w.store.PinWorkflowImage(w.ctx, w.root, "image-b"); err != nil || pinned != "" {
		t.Fatalf("root has an image pin: %q, %v", pinned, err)
	}
	w.step(crew, "kept", contract.WorkflowStepDone, "")
	w.step(crew, "failed", contract.WorkflowStepFailed, "refused")
	if _, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Resume: started.Run}); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("resuming a live run: %+v", problem)
	}
	if _, problem := w.TurnOutput(w.ctx, crew, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Model: "m"}); problem != nil {
		t.Fatal(problem)
	}
	next, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Resume: started.Run})
	if problem != nil {
		t.Fatal(problem)
	}
	second := contract.Principal{Kind: contract.PrincipalSession, Session: next.Session}
	stored, problem := w.WorkflowStepStart(w.ctx, second, contract.WorkflowStepStartRequest{Key: "kept", Kind: contract.WorkflowStepAgent})
	if problem != nil || stored.State != contract.WorkflowStepDone || stored.Result != `"ok"` {
		t.Fatalf("kept step %+v, %+v", stored, problem)
	}
	if retried, problem := w.WorkflowStepStart(w.ctx, second, contract.WorkflowStepStartRequest{Key: "failed", Kind: contract.WorkflowStepAgent}); problem != nil || retried.State != "" {
		t.Fatalf("a failed step must rerun: %+v, %+v", retried, problem)
	}
	var run store.WorkflowRun
	var err error
	if run, err = w.store.WorkflowRun(w.ctx, next.Run); err != nil || run.Commit != "abc" || run.Image != "image-a" {
		t.Fatalf("run %+v, %v", run, err)
	}
	// The kept steps cost the new run nothing.
	if summary, err := w.store.WorkflowSummary(w.ctx, next.Run); err != nil || summary.Usage.InputTokens != 0 {
		t.Fatalf("summary %+v, %v", summary, err)
	}
}

func TestOnlyACrewWritesAJournal(t *testing.T) {
	w := setupWorkflow(t)
	root := contract.Principal{Kind: contract.PrincipalSession, Session: w.root}
	if _, problem := w.WorkflowStepStart(w.ctx, root, contract.WorkflowStepStartRequest{Key: "k", Kind: contract.WorkflowStepAgent}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("%+v", problem)
	}
}

// A step change reaches an open page, leaves no event behind and wakes nobody, and a replay of a stored step sends nothing.
func TestAStepChangeIsLiveAndQuiet(t *testing.T) {
	w := setupWorkflow(t)
	_, crew := w.start(w.owner, workflowScript)
	w.turn(crew)
	count := func(query string) (n int) {
		if err := w.store.Read().QueryRowContext(w.ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	events, deliveries := count("SELECT COUNT(*) FROM events"), count("SELECT COUNT(*) FROM deliveries")
	subscription, live := w.live.Subscribe(w.room, 16)
	defer w.live.Unsubscribe(subscription)
	w.step(crew, "a", contract.WorkflowStepRunning, "")
	if _, problem := w.WorkflowStepTranscript(w.ctx, crew, contract.WorkflowStepTranscriptRequest{Key: "a", Transcript: "a.jsonl"}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := w.WorkflowStepTranscript(w.ctx, crew, contract.WorkflowStepTranscriptRequest{Key: "a", Transcript: "../a.jsonl"}); problem == nil {
		t.Fatal("a path outside the crew's folder was taken")
	}
	if _, problem := w.WorkflowStepEnd(w.ctx, crew, contract.WorkflowStepEndRequest{Key: "a", State: contract.WorkflowStepFailed, Reason: "refused: password=hunter2hunter2", Transcript: "a.jsonl", Usage: contract.Usage{OutputTokens: 5}}); problem != nil {
		t.Fatal(problem)
	}
	if stored, problem := w.WorkflowStepStart(w.ctx, crew, contract.WorkflowStepStartRequest{Key: "a", Kind: contract.WorkflowStepAgent}); problem != nil || stored.State != contract.WorkflowStepFailed {
		t.Fatalf("a stored step: %+v, %+v", stored, problem)
	}
	var got []contract.WorkflowStep
	for len(live) > 0 {
		if body, ok := (<-live).Body.(contract.WorkflowStepChanged); ok {
			got = append(got, body.Step)
		}
	}
	if len(got) != 3 || got[0].State != contract.WorkflowStepRunning || got[0].Transcript != "" || got[1].Transcript != "a.jsonl" || got[2].State != contract.WorkflowStepFailed || !strings.HasPrefix(got[2].Reason, "refused") || strings.Contains(got[2].Reason, "hunter2") || got[2].Usage.OutputTokens != 5 {
		t.Fatalf("live steps %+v", got)
	}
	if after, sent := count("SELECT COUNT(*) FROM events"), count("SELECT COUNT(*) FROM deliveries"); after != events || sent != deliveries {
		t.Fatalf("events %d to %d, deliveries %d to %d", events, after, deliveries, sent)
	}
	if status, problem := w.WorkflowStatus(w.ctx, w.owner, contract.WorkflowStatusRequest{Session: crew.Session}); problem != nil || strings.Contains(status.Steps[0].Reason, "hunter2") {
		t.Fatalf("status %+v, %+v", status, problem)
	}
}

func TestWorkflowListOmitsResultButStatusKeepsIt(t *testing.T) {
	w := setupWorkflow(t)
	started, _ := w.start(w.owner, workflowScript)
	if err := w.store.Write(w.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(w.ctx, "UPDATE workflow_runs SET state='done',result=? WHERE id=?", strings.Repeat("result", 10000), started.Run)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	list, problem := w.WorkflowList(w.ctx, w.owner, contract.WorkflowListRequest{Room: w.room})
	if problem != nil || len(list.Runs) != 1 {
		t.Fatalf("%+v %+v", list, problem)
	}
	if list.Runs[0].Result != "" {
		t.Fatal("list includes full result")
	}
	status, problem := w.WorkflowStatus(w.ctx, w.owner, contract.WorkflowStatusRequest{Run: started.Run})
	if problem != nil || status.Run.Result != strings.Repeat("result", 10000) {
		t.Fatalf("status lost result: %v", problem)
	}
}

func TestWorkflowSteerResolvesOneRunningWorker(t *testing.T) {
	w := setupWorkflow(t)
	started, run := w.start(w.owner, workflowScript)
	w.step(run, "alpha-key", contract.WorkflowStepRunning, "")
	w.step(run, "beta-key", contract.WorkflowStepRunning, "")
	w.step(run, "alpha-other", contract.WorkflowStepRunning, "")
	for _, step := range []string{"", "missing", "alpha"} {
		_, problem := w.WorkflowSteer(w.ctx, w.owner, contract.WorkflowSteerRequest{Run: started.Run, Step: step, Text: "change direction"})
		if problem == nil || !strings.Contains(problem.Message, "alpha-key") || !strings.Contains(problem.Message, "beta-key") {
			t.Fatalf("refusal: %+v", problem)
		}
	}
	posted, problem := w.WorkflowSteer(w.ctx, w.owner, contract.WorkflowSteerRequest{Run: started.Run, Step: "alpha-k", Text: "change direction"})
	if problem != nil {
		t.Fatal(problem)
	}
	event, err := w.store.Event(w.ctx, posted.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if post := event.Body.(contract.PostCreated); post.WorkflowStep != "alpha-key" {
		t.Fatalf("post: %+v", post)
	}
}

func TestWorkflowPauseIsStoredAndShownInStatus(t *testing.T) {
	w := setupWorkflow(t)
	started, _ := w.start(w.owner, workflowScript)
	if _, problem := w.WorkflowPause(w.ctx, w.owner, contract.WorkflowPauseRequest{Run: started.Run}); problem != nil {
		t.Fatal(problem)
	}
	status, problem := w.WorkflowStatus(w.ctx, w.owner, contract.WorkflowStatusRequest{Run: started.Run})
	if problem != nil || !status.Run.Paused {
		t.Fatalf("status %+v: %v", status, problem)
	}
	stored, err := w.store.WorkflowRun(w.ctx, started.Run)
	if err != nil || !stored.Paused {
		t.Fatalf("stored %+v: %v", stored, err)
	}
	if _, problem := w.WorkflowUnpause(w.ctx, w.owner, contract.WorkflowUnpauseRequest{Run: started.Run}); problem != nil {
		t.Fatal(problem)
	}
	stored, err = w.store.WorkflowRun(w.ctx, started.Run)
	if err != nil || stored.Paused {
		t.Fatalf("stored %+v: %v", stored, err)
	}
}

func TestPostToEndedWorkflowNamesWhyItCannotReceive(t *testing.T) {
	w := setupWorkflow(t)
	started, worker := w.start(w.owner, workflowScript)
	w.endRun(worker)
	_, problem := w.Post(w.ctx, w.owner, contract.PostRequest{Room: w.room, Text: "late direction", Mentions: []string{string(started.Session)}})
	if problem == nil || problem.Code != contract.ErrConflict || !strings.Contains(problem.Message, "workflow run has ended") {
		t.Fatalf("refusal: %+v", problem)
	}
}

func TestWorkflowStepPauseUsesWorkerResolutionAndPersists(t *testing.T) {
	w := setupWorkflow(t)
	started, run := w.start(w.owner, workflowScript)
	w.step(run, "alpha-key", contract.WorkflowStepRunning, "")
	w.step(run, "beta-key", contract.WorkflowStepRunning, "")
	for _, step := range []string{"missing"} {
		if _, problem := w.WorkflowPause(w.ctx, w.owner, contract.WorkflowPauseRequest{Run: started.Run, Step: step}); problem == nil || !strings.Contains(problem.Message, "alpha-key") {
			t.Fatalf("refusal: %+v", problem)
		}
	}
	if _, problem := w.WorkflowPause(w.ctx, w.owner, contract.WorkflowPauseRequest{Run: started.Run, Step: "alpha-k"}); problem != nil {
		t.Fatal(problem)
	}
	stored, err := w.store.WorkflowRun(w.ctx, started.Run)
	if err != nil || stored.Paused || len(stored.PausedSteps) != 1 || stored.PausedSteps[0] != "alpha-key" {
		t.Fatalf("stored: %+v %v", stored, err)
	}
	// A box rebuild reads the durable run, not the previous box's gate.
	workflow, err := w.boxWorkflow(w.ctx, stored, 16<<30)
	if err != nil || len(workflow.PausedSteps) != 1 {
		t.Fatalf("box pause: %+v %v", workflow, err)
	}
	if _, problem := w.WorkflowUnpause(w.ctx, w.owner, contract.WorkflowUnpauseRequest{Run: started.Run, Step: "alpha-key"}); problem != nil {
		t.Fatal(problem)
	}
	stored, err = w.store.WorkflowRun(w.ctx, started.Run)
	if err != nil || len(stored.PausedSteps) != 0 {
		t.Fatalf("stored: %+v %v", stored, err)
	}
}

func TestWorkflowControlsAcceptSessionName(t *testing.T) {
	w := setupWorkflow(t)
	started, run := w.start(w.owner, workflowScript)
	w.step(run, "alpha-key", contract.WorkflowStepRunning, "")
	session, err := w.store.Session(w.ctx, started.Session)
	if err != nil {
		t.Fatal(err)
	}
	name := contract.WorkflowRunID(session.Name)
	if _, problem := w.WorkflowPause(w.ctx, w.owner, contract.WorkflowPauseRequest{Run: name}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := w.WorkflowUnpause(w.ctx, w.owner, contract.WorkflowUnpauseRequest{Run: name}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := w.WorkflowSteer(w.ctx, w.owner, contract.WorkflowSteerRequest{Run: name, Step: "alpha-key", Text: "redirect"}); problem != nil {
		t.Fatal(problem)
	}
}

func TestPauseEndedWorkflowReturnsEnded(t *testing.T) {
	w := setupWorkflow(t)
	started, worker := w.start(w.owner, workflowScript)
	w.endRun(worker)
	for _, step := range []string{"", "alpha-key"} {
		if err := w.store.PauseWorkflow(w.ctx, started.Run, true, step); err != store.ErrWorkflowEnded {
			t.Fatalf("pause step %q: %v, want ErrWorkflowEnded", step, err)
		}
	}
	if _, problem := w.WorkflowPause(w.ctx, w.owner, contract.WorkflowPauseRequest{Run: started.Run}); problem == nil || problem.Message != "that run has ended" {
		t.Fatalf("pause refusal: %+v", problem)
	}
}

func TestEndedStepAndRunUnpauseClearWorkerHolds(t *testing.T) {
	w := setupWorkflow(t)
	started, run := w.start(w.owner, workflowScript)
	for _, key := range []string{"alpha", "beta"} {
		w.step(run, key, contract.WorkflowStepRunning, "")
		if err := w.store.PauseWorkflow(w.ctx, started.Run, true, key); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.store.EndWorkflowStep(w.ctx, started.Run, contract.WorkflowStepEndRequest{Key: "alpha", State: contract.WorkflowStepDone}, time.Now()); err != nil {
		t.Fatal(err)
	}
	stored, err := w.store.WorkflowRun(w.ctx, started.Run)
	if err != nil || !slices.Equal(stored.PausedSteps, []string{"beta"}) {
		t.Fatalf("step cleanup: %+v %v", stored.PausedSteps, err)
	}
	if _, problem := w.WorkflowUnpause(w.ctx, w.owner, contract.WorkflowUnpauseRequest{Run: started.Run}); problem != nil {
		t.Fatal(problem)
	}
	stored, err = w.store.WorkflowRun(w.ctx, started.Run)
	if err != nil || len(stored.PausedSteps) != 0 || stored.Paused {
		t.Fatalf("run unpause: %+v %v", stored, err)
	}
}
