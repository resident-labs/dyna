package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/store"
)

// fixtureStep is one invented journal row. A zero took leaves it running.
type fixtureStep struct {
	key, name, phase string
	kind             contract.WorkflowStepKind
	profile          contract.ProfileName
	took             time.Duration
	reason, result   string
	waiting          bool // a worker with no file yet
}

func agentStep(key, name, phase string, profile contract.ProfileName, took time.Duration) fixtureStep {
	return fixtureStep{key: key, name: name, phase: phase, kind: contract.WorkflowStepAgent, profile: profile, took: took, result: `"ok"`}
}

// auditSteps is a large finished run: one long phase, a merge with a conflict, and failures, so a big page can be judged.
func auditSteps(session contract.SessionID) []fixtureStep {
	steps := []fixtureStep{{key: "base", name: "run: git log -1 --format=%h", phase: "Survey", kind: contract.WorkflowStepCommand, took: 40 * time.Millisecond, result: `{"code":0,"stdout":"daa14cb","stderr":"","durationMs":40}`}}
	for i, page := range []string{"home", "pricing", "docs", "blog", "careers", "support"} {
		steps = append(steps, agentStep("list-"+page, "List the links on "+page, "Survey", "quick", time.Duration(4+i)*time.Second))
	}
	for i := range 120 {
		step := agentStep(fmt.Sprintf("page-%d", i), fmt.Sprintf("Review page %d of the site for broken links and stale text", i+1), "Review pages", []contract.ProfileName{"fake", "deep"}[i%2], time.Duration(20+i%40)*time.Second)
		if i%47 == 13 {
			step.reason, step.result = "the worker's turn failed: the provider returned 529 three times", ""
		}
		steps = append(steps, step)
	}
	for i := range 20 {
		steps = append(steps, agentStep(fmt.Sprintf("fix-%d", i), fmt.Sprintf("fix-%d", i+1), "Fix", "deep", time.Duration(60+i)*time.Second))
	}
	merge, _ := json.Marshal(contract.WorkflowMerge{Branch: fmt.Sprintf("v2/%s-run", session), Commit: "18b5fa2b8d9825dcdd238bbdcb671334c70eb35b", Merged: []string{"fix-1", "fix-2", "fix-3", "fix-4"},
		Conflicts: []contract.WorkflowConflict{{Step: "fix-5", Branch: fmt.Sprintf("v2/%s-e3a70df0216b", session), Files: []string{"web/src/pages/pricing.ts", "web/src/lib/links.ts"}}}})
	return append(steps,
		fixtureStep{key: "merge", name: "run: merge fix-1, fix-2, fix-3, fix-4, fix-5", phase: "Merge", kind: contract.WorkflowStepCommand, took: 3 * time.Second, result: string(merge)},
		fixtureStep{key: "conflict", name: "merge conflict: fix-5", phase: "Merge", kind: contract.WorkflowStepLog, took: time.Millisecond, reason: "conflicts in web/src/pages/pricing.ts, web/src/lib/links.ts"})
}

func triageSteps() []fixtureStep {
	return []fixtureStep{
		agentStep("issues", "Read the open issues", "Survey", "quick", 7*time.Second),
		agentStep("logs", "Read last night's logs", "Survey", "quick", 11*time.Second),
		{key: "head", name: "run: git log -1 --format=%h", phase: "Survey", kind: contract.WorkflowStepCommand, took: 30 * time.Millisecond, result: `{"code":0,"stdout":"daa14cb","stderr":"","durationMs":30}`},
		{key: "note", name: "log: 12 issues to look at", phase: "Survey", kind: contract.WorkflowStepLog, took: time.Millisecond, result: "null"},
		agentStep("flaky", "Find why the sync test is flaky", "Fix", "deep", 0),
		agentStep("banner", "Fix the banner on narrow screens", "Fix", "fake", 0),
		{key: "refused", name: "Update the release notes", phase: "Fix", kind: contract.WorkflowStepAgent, profile: "fake", took: 22 * time.Second, reason: "no reply matched the schema in 3 attempts: it is not JSON: unexpected end of input"},
		{key: "waiting", name: "Check the fix against the docs", phase: "Fix", kind: contract.WorkflowStepAgent, profile: "quick", waiting: true},
	}
}

func buildSteps() []fixtureStep {
	return []fixtureStep{
		agentStep("plan", "Plan the build", "Prepare", "deep", 19*time.Second),
		{key: "deps", name: "run: npm ci", phase: "Prepare", kind: contract.WorkflowStepCommand, took: 41 * time.Second, result: `{"code":0,"stdout":"","stderr":"","durationMs":41000}`},
		{key: "tests", name: "run: npm test", phase: "Check", kind: contract.WorkflowStepCommand, took: 73 * time.Second, reason: "exit code 1: 2 tests failed\n  sync client retries after a 503\n  banner keeps its height"},
	}
}

// fixtureJournal writes a run's rows with invented times, the newest ending about now. Running rows stay open.
func fixtureJournal(ctx context.Context, st *store.Store, run contract.WorkflowRunID, steps []fixtureStep, start time.Time) {
	for i, step := range steps {
		at := start.Add(time.Duration(i) * 3 * time.Second)
		_, err := st.StartWorkflowStep(ctx, run, contract.WorkflowStepStartRequest{Key: step.key, Kind: step.kind, Name: step.name, Phase: step.phase, Profile: step.profile, Detail: "Invented work: " + step.name}, at)
		fatal(err)
		state := contract.WorkflowStepDone
		if step.reason != "" {
			state = contract.WorkflowStepFailed
		}
		file := ""
		if step.kind == contract.WorkflowStepAgent && !step.waiting {
			file = "fixture.jsonl"
		}
		if step.took == 0 {
			fatal(st.SetWorkflowStepTranscript(ctx, run, step.key, file))
			continue
		}
		used := contract.Usage{}
		if step.kind == contract.WorkflowStepAgent {
			used = contract.Usage{InputTokens: 12 + int64(i), OutputTokens: 340 + int64(i)*7, CacheReadTokens: 12800, CacheWriteTokens: 1400 + int64(i)*90}
		}
		fatal(st.EndWorkflowStep(ctx, run, contract.WorkflowStepEndRequest{Key: step.key, State: state, Reason: step.reason, Result: step.result, Transcript: file, Usage: used}, at.Add(step.took)))
	}
}

// endFixtureRun leaves a run as a real end does: its journal closed, then its crew.
func endFixtureRun(ctx context.Context, s *engine.Service, st *store.Store, room contract.RoomID, started contract.WorkflowRunResponse, name string, state contract.WorkflowState, result string, at time.Time) {
	summary, err := st.WorkflowSummary(ctx, started.Run)
	fatal(err)
	summary.Session, summary.Name, summary.State = started.Session, name, state
	fatal(st.EndWorkflow(ctx, started.Run, state, result, contract.Event{ID: contract.EventID("seed-" + string(started.Run)), Room: room, Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: summary}))
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: started.Session}); e != nil {
		log.Fatal(e.Message)
	}
}

// seedRuns adds a running crew, whose page moves by itself, a large finished run, a failed one and one whose box is lost. The
// running crew's fake turn waits until it is stopped, so Stop acts on it for real.
func seedRuns(ctx context.Context, s *engine.Service, st *store.Store, room contract.RoomID) {
	start := func(name, description string) (contract.WorkflowRunResponse, contract.Principal) {
		script := fmt.Sprintf("export const meta = { name: '%s', description: '%s', profiles: ['fake', 'deep', 'quick'] }\nreturn 1\n", name, description)
		started, e := s.WorkflowRun(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.WorkflowRunRequest{Room: room, Script: script})
		if e != nil {
			log.Fatal(e.Message)
		}
		return started, contract.Principal{Kind: contract.PrincipalSession, Session: started.Session}
	}
	now := time.Now()
	audit, _ := start("site-audit", "Review every page for broken links, then fix what it finds")
	fixtureJournal(ctx, st, audit.Run, auditSteps(audit.Session), now.Add(-2*time.Hour))
	endFixtureRun(ctx, s, st, room, audit, "site-audit", contract.WorkflowDone, `{"reviewed":120,"broken":14,"fixed":4}`, now.Add(-100*time.Minute))
	build, _ := start("nightly-build", "Build and test the site, then tag it")
	fixtureJournal(ctx, st, build.Run, buildSteps(), now.Add(-time.Hour))
	endFixtureRun(ctx, s, st, room, build, "nightly-build", contract.WorkflowFailed, "Error: run failed: exit code 1: 2 tests failed\n    at script:14:3", now.Add(-58*time.Minute))
	triage, crew := start("triage", "Sort the open issues and fix what is quick")
	fixtureJournal(ctx, st, triage.Run, triageSteps(), now.Add(-4*time.Minute))
	go advanceRun(ctx, s, crew)
	lost, _ := start("docs-check", "Check the docs against the code")
	fixtureJournal(ctx, st, lost.Run, []fixtureStep{agentStep("api", "Check the API reference", "", "quick", 12*time.Second), agentStep("guide", "Check the getting started guide", "", "fake", 0)}, now.Add(-3*time.Minute))
}

// advanceRun moves the running crew through the real journal calls, so the page's live updates can be watched.
func advanceRun(ctx context.Context, s *engine.Service, crew contract.Principal) {
	steps := func(yield func(func() *contract.Error) bool) {
		if !yield(func() *contract.Error {
			_, e := s.WorkflowStepTranscript(ctx, crew, contract.WorkflowStepTranscriptRequest{Key: "waiting", Transcript: "fixture.jsonl"})
			return e
		}) {
			return
		}
		for _, key := range []string{"flaky", "banner", "waiting"} {
			if !yield(func() *contract.Error {
				_, e := s.WorkflowStepEnd(ctx, crew, contract.WorkflowStepEndRequest{Key: key, State: contract.WorkflowStepDone, Result: `"ok"`, Transcript: "fixture.jsonl", Usage: contract.Usage{InputTokens: 20, OutputTokens: 900, CacheReadTokens: 14000, CacheWriteTokens: 2200}})
				return e
			}) {
				return
			}
		}
		for i := range 1000 {
			key := fmt.Sprintf("late-%d", i)
			if !yield(func() *contract.Error {
				_, e := s.WorkflowStepStart(ctx, crew, contract.WorkflowStepStartRequest{Key: key, Kind: contract.WorkflowStepCommand, Name: fmt.Sprintf("run: npm test -- part-%d", i+1), Phase: "Check", Detail: "npm test"})
				return e
			}) || !yield(func() *contract.Error {
				_, e := s.WorkflowStepEnd(ctx, crew, contract.WorkflowStepEndRequest{Key: key, State: contract.WorkflowStepDone, Result: `{"code":0,"stdout":"","stderr":"","durationMs":3000}`})
				return e
			}) {
				return
			}
		}
	}
	for step := range steps {
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		if step() != nil {
			return // Stopped, so the journal takes no more.
		}
	}
}

// WorkflowStatus gives the fixture's runs a repository, which the fixture room does not have, so their links show, and the start
// of their invented steps.
func (d devService) WorkflowStatus(ctx context.Context, p contract.Principal, req contract.WorkflowStatusRequest) (contract.WorkflowStatusResponse, *contract.Error) {
	status, problem := d.Service.WorkflowStatus(ctx, p, req)
	if problem == nil {
		status.Run.Repo, status.Run.Commit = "acme/site", "daa14cb880ff4571af8e2cd2c6b0ed8e70bc4682"
		if len(status.Steps) > 0 {
			status.Run.Started = status.Steps[0].Started
		}
	}
	return status, problem
}

// fixtureWorker answers a worker's transcript from the long fixture, since the fake harness writes no file: a running step gets
// the turn that is still running.
func fixtureWorker(ctx context.Context, d devService, p contract.Principal, req contract.TranscriptReadRequest) (contract.TranscriptReadResponse, *contract.Error) {
	status, problem := d.Service.WorkflowStatus(ctx, p, contract.WorkflowStatusRequest{Session: req.Session})
	if problem != nil {
		return contract.TranscriptReadResponse{}, problem
	}
	for _, step := range status.Steps {
		if step.Key != req.Step || step.Transcript == "" {
			continue
		}
		turns := fixtureTurns()
		turn := turns[len(turns)-1]
		for _, candidate := range turns {
			if (candidate.Ended == nil) == step.Ended.IsZero() {
				turn = candidate
				break
			}
		}
		turn.Started, turn.Outcome, turn.Inputs = step.Started, "", []contract.TranscriptInput{{ID: contract.EventID("step:" + step.Key), At: step.Started, Type: "post.created", From: contract.Principal{Kind: contract.PrincipalSession, Session: req.Session}, Text: "Invented work: " + step.Name}}
		if !step.Ended.IsZero() {
			turn.Ended, turn.DurationMS = &step.Ended, step.Ended.Sub(step.Started).Milliseconds()
		}
		return contract.TranscriptReadResponse{Turns: []contract.TranscriptTurn{turn}}, nil
	}
	return contract.TranscriptReadResponse{}, nil
}
