package engine

import (
	"context"
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

func (w workflowCase) ended(session contract.SessionID) contract.TurnEnded {
	w.t.Helper()
	listed, problem := w.EventsList(w.ctx, w.owner, contract.EventsListRequest{Room: w.room, Types: []contract.EventType{"turn.ended"}, Limit: 100})
	if problem != nil {
		w.t.Fatal(problem)
	}
	for _, event := range listed.Events {
		if body := event.Body.(contract.TurnEnded); body.Session == session {
			return body
		}
	}
	w.t.Fatal("no turn ended")
	return contract.TurnEnded{}
}

func TestOnlyTheOwnerOrRootStartsAWorkflow(t *testing.T) {
	w := setupWorkflow(t)
	if _, problem := w.WorkflowRun(w.ctx, contract.Principal{Kind: contract.PrincipalSession, Session: w.root}, contract.WorkflowRunRequest{Script: workflowScript}); problem != nil {
		t.Fatalf("root in its own room: %+v", problem)
	}
	_, crew := w.start(w.owner, workflowScript)
	if _, problem := w.WorkflowRun(w.ctx, crew, contract.WorkflowRunRequest{Script: workflowScript}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("a child session: %+v", problem)
	}
}

func TestAScriptIsCheckedBeforeAnythingStarts(t *testing.T) {
	w := setupWorkflow(t)
	sessions := func() (n int) {
		if err := w.store.Read().QueryRowContext(w.ctx, "SELECT COUNT(*) FROM sessions").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := sessions()
	for name, script := range map[string]string{
		"no meta":         "return 1",
		"unknown profile": "export const meta = { name: 'x', profiles: ['nobody'] }",
		"unknown key":     "export const meta = { name: 'x', profiles: ['fast'], deterministic: true }",
		"no name":         "export const meta = { profiles: ['fast'] }",
		"no profiles":     "export const meta = { name: 'x' }",
		"computed":        "export const meta = { name: 'x' + y, profiles: ['fast'] }",
		"unknown size":    "export const meta = { name: 'x', profiles: ['fast'], size: 'huge' }",
		"unlisted repo":   "export const meta = { name: 'x', profiles: ['fast'], repo: 'a/b' }",
		"bad name":        "export const meta = { name: 'not a name', profiles: ['fast'] }",
	} {
		if _, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: script}); problem == nil || problem.Code != contract.ErrInvalid {
			t.Errorf("%s: %+v", name, problem)
		}
	}
	if _, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Args: "{not json"}); problem == nil {
		t.Error("bad args started a run")
	}
	if sessions() != before {
		t.Fatal("a refused script left a session")
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

func TestAHelloCarriesTheRunWithItsProfilesInOrder(t *testing.T) {
	w := setupWorkflow(t)
	_, crew := w.start(w.owner, workflowScript)
	passLifecycle(t, w.Service, w.ctx)
	hello, problem := w.BoxHello(w.ctx, crew, contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	workflow := hello.Workflow
	if workflow == nil || workflow.Script != workflowScript || len(workflow.Profiles) != 2 || workflow.Profiles[0].Name != "fast" || workflow.Profiles[1].Model.ID != crewDeepModel {
		t.Fatalf("workflow %+v", workflow)
	}
	if workflow.Workers < 4 {
		t.Fatalf("%d workers", workflow.Workers)
	}
	root := contract.Principal{Kind: contract.PrincipalSession, Session: w.root}
	if hello, problem = w.BoxHello(w.ctx, root, contract.BoxHelloRequest{}); problem != nil || hello.Workflow != nil {
		t.Fatalf("root's hello %+v, %+v", hello.Workflow, problem)
	}
}

// The script's value is the caller's final reply, the run's end names what failed, and the crew closes.
func TestARunEndsInAFinalReplyAndAClose(t *testing.T) {
	w := setupWorkflow(t)
	root := contract.Principal{Kind: contract.PrincipalSession, Session: w.root}
	started, crew := w.start(root, workflowScript)
	start := w.turn(crew)
	w.step(crew, "good", contract.WorkflowStepDone, "")
	w.step(crew, "bad", contract.WorkflowStepFailed, "the model refused")
	w.step(crew, "forgotten", contract.WorkflowStepRunning, "")
	// A post on the way is a worker's or the script's. It must not stand in for the run's answer.
	if _, problem := w.Post(w.ctx, crew, contract.PostRequest{Room: w.room, Text: "halfway", Mentions: []string{"@root"}}); problem != nil {
		t.Fatal(problem)
	}
	output := contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Final: `{"found":2}`, Model: "m"}
	if _, problem := w.TurnOutput(w.ctx, crew, output); problem != nil {
		t.Fatal(problem)
	}
	status, problem := w.WorkflowStatus(w.ctx, root, contract.WorkflowStatusRequest{Run: started.Run})
	if problem != nil {
		t.Fatal(problem)
	}
	if status.Run.State != contract.WorkflowDone || status.Run.Result != `{"found":2}` || status.Run.Failed != 2 || status.Run.Usage.InputTokens != 14 {
		t.Fatalf("run %+v", status.Run)
	}
	ended := w.ended(started.Session)
	if !strings.HasPrefix(ended.Final, `{"found":2}`) || !strings.Contains(ended.Final, "Failed steps (2): bad, forgotten") || ended.Usage.InputTokens != 14 {
		t.Fatalf("reply %+v", ended)
	}
	if len(ended.Replies) != 1 || ended.Replies[0].Reply.Target != root || ended.Replies[0].Posted != "" {
		t.Fatalf("replies %+v", ended.Replies)
	}
	var woken int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT COUNT(*) FROM deliveries WHERE to_id=? AND wake=1 AND seq IN (SELECT seq FROM events WHERE type='turn.ended')", w.root).Scan(&woken); err != nil || woken != 1 {
		t.Fatalf("%d wakes for the result, %v", woken, err)
	}
	var state string
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT state FROM sessions WHERE id=?", started.Session).Scan(&state); err != nil || state != "closing" {
		t.Fatalf("session %s, %v", state, err)
	}
	// A retried report changes nothing.
	if _, problem := w.TurnOutput(w.ctx, crew, output); problem != nil {
		t.Fatalf("retry: %+v", problem)
	}
	runs, problem := w.WorkflowList(w.ctx, w.owner, contract.WorkflowListRequest{Room: w.room})
	if problem != nil || len(runs.Runs) != 1 || runs.Runs[0].Failed != 2 {
		t.Fatalf("list %+v, %+v", runs, problem)
	}
}

// An owner has no session to wake, so the run's crew sits under root and its reply is the owner's.
func TestAnOwnersRunAnswersTheOwner(t *testing.T) {
	w := setupWorkflow(t)
	started, crew := w.start(w.owner, workflowScript)
	start := w.turn(crew)
	if _, problem := w.TurnOutput(w.ctx, crew, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Model: "m"}); problem != nil {
		t.Fatal(problem)
	}
	ended := w.ended(started.Session)
	if ended.Final != "Workflow sweep finished." || len(ended.Replies) != 1 || ended.Replies[0].Reply.Target.Kind != contract.PrincipalOwner {
		t.Fatalf("reply %+v", ended)
	}
}

// A script that throws is the run's result: nothing retries it, the caller hears, and the crew closes.
func TestAThrownScriptFailsTheRunOnce(t *testing.T) {
	w := setupWorkflow(t)
	root := contract.Principal{Kind: contract.PrincipalSession, Session: w.root}
	started, crew := w.start(root, workflowScript)
	start := w.turn(crew)
	if _, problem := w.TurnOutput(w.ctx, crew, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnFailed, Error: "Error: nothing found", Cause: contract.CauseWorkflowFailed, Model: "m"}); problem != nil {
		t.Fatal(problem)
	}
	run, err := w.store.WorkflowRun(w.ctx, started.Run)
	if err != nil || run.State != contract.WorkflowFailed {
		t.Fatalf("run %+v, %v", run, err)
	}
	ended := w.ended(started.Session)
	if ended.Outcome != contract.TurnFailed || !strings.Contains(ended.Error, "nothing found") || len(ended.Replies) != 1 {
		t.Fatalf("turn %+v", ended)
	}
	var pending int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT COUNT(*) FROM deliveries WHERE to_id=? AND state='pending'", started.Session).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("%d inputs handed back, %v", pending, err)
	}
}

// A box that could not set up is a failed session, not a failed run: session.failed tells the parent and the run can restart.
func TestAFailedSetupLeavesTheRunGoing(t *testing.T) {
	w := setupWorkflow(t)
	started, crew := w.start(w.owner, workflowScript)
	start := w.turn(crew)
	if _, problem := w.TurnOutput(w.ctx, crew, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnFailed, Error: "clone failed", Cause: contract.CauseSetupFailed, Model: "m"}); problem != nil {
		t.Fatal(problem)
	}
	if run, err := w.store.WorkflowRun(w.ctx, started.Run); err != nil || run.State != contract.WorkflowRunning {
		t.Fatalf("run %+v, %v", run, err)
	}
}

// A stop that lands before the script began still stops the run: nothing else would.
func TestAStopBeforeTheScriptBeganEndsTheRun(t *testing.T) {
	w := setupWorkflow(t)
	started, crew := w.start(w.owner, workflowScript)
	start := w.turn(crew)
	if _, problem := w.TurnOutput(w.ctx, crew, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnStopped, Cause: contract.CauseNotStarted, Model: "m"}); problem != nil {
		t.Fatal(problem)
	}
	if run, err := w.store.WorkflowRun(w.ctx, started.Run); err != nil || run.State != contract.WorkflowStopped {
		t.Fatalf("run %+v, %v", run, err)
	}
	var pending int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT COUNT(*) FROM deliveries WHERE to_id=? AND state='pending'", started.Session).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("%d inputs handed back, %v", pending, err)
	}
}

func TestClosingACrewStopsItsRun(t *testing.T) {
	w := setupWorkflow(t)
	started, crew := w.start(w.owner, workflowScript)
	w.turn(crew)
	w.step(crew, "forgotten", contract.WorkflowStepRunning, "")
	if _, problem := w.SessionClose(w.ctx, w.owner, contract.SessionCloseRequest{Session: started.Session}); problem != nil {
		t.Fatal(problem)
	}
	status, problem := w.WorkflowStatus(w.ctx, w.owner, contract.WorkflowStatusRequest{Run: started.Run})
	if problem != nil {
		t.Fatal(problem)
	}
	if status.Run.State != contract.WorkflowStopped || status.Run.Failed != 1 {
		t.Fatalf("run %+v", status.Run)
	}
	listed, problem := w.EventsList(w.ctx, w.owner, contract.EventsListRequest{Room: w.room, Types: []contract.EventType{"workflow.ended"}, Limit: 10})
	if problem != nil || len(listed.Events) != 1 {
		t.Fatalf("%+v, %+v", listed, problem)
	}
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

func TestParseWorkflowMeta(t *testing.T) {
	meta, err := parseWorkflowMeta(`// A sweep of the decoders.
/* written by hand */
export const meta = {
	name: "sweep", // the name
	'description': 'it\'s a sweep',
	profiles: ['fast', "deep",],
	size: ` + "`large`" + `,
	repo: 'owner/name',
};
const later = Date.now()`)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "sweep" || meta.Description != "it's a sweep" || !slices.Equal(meta.Profiles, []contract.ProfileName{"fast", "deep"}) || meta.Size != contract.BoxSizeLarge || meta.Repo != "owner/name" {
		t.Fatalf("meta %+v", meta)
	}
	for _, script := range []string{
		"const meta = { name: 'x', profiles: ['a'] }",
		"export const meta = load()",
		"export const meta = { name: 'x', profiles: ['a'] }.valueOf()",
		"export const meta = { name: `x${y}`, profiles: ['a'] }",
		"export const meta = { name: 'x', ...more }",
		"export const meta = { name: 'x', profiles: ['a']",
		"export const meta = { name: 'x', profiles: 'a' }",
	} {
		if _, err := parseWorkflowMeta(script); err == nil {
			t.Errorf("accepted %s", script)
		}
	}
}
