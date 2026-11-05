package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/room"
)

const (
	workflowScriptBytes = 1 << 20
	// workerMemory is what one Pi process of a crew is given. The box's memory.high divided by it bounds the fan-out.
	workerMemory = 512 << 20
)

func (s *Service) workflowRoom(ctx context.Context, caller contract.Principal, room contract.RoomID) (contract.RoomID, *contract.Error) {
	if room == "" && caller.Kind == contract.PrincipalSession {
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return "", failure(err)
		}
		room = session.Room
	}
	if room == "" {
		return "", invalid("room is required")
	}
	return room, s.roomAccess(ctx, caller, room)
}

// workflowScript reads a script's meta, with its profiles and its repository among the room's.
func (s *Service) workflowScript(ctx context.Context, roomID contract.RoomID, script string) (workflowMeta, contract.RepoName, *contract.Error) {
	if len(script) > workflowScriptBytes {
		return workflowMeta{}, "", invalid("the script is over 1 MiB")
	}
	meta, err := parseWorkflowMeta(script)
	if err != nil {
		return meta, "", invalid(err.Error())
	}
	for _, name := range meta.Profiles {
		if _, _, err := s.profiles.Model(name); err != nil {
			return meta, "", invalid("unknown profile " + string(name))
		}
	}
	repos, err := s.store.RoomRepos(ctx, roomID)
	if err != nil {
		return meta, "", failure(err)
	}
	repo, err := room.RepoForSession(repos, meta.Repo, "")
	if err != nil {
		return meta, "", invalid(err.Error())
	}
	return meta, repo, nil
}

// WorkflowRun checks a script and starts its crew: one child session of the caller, in one box, whose only turn runs it.
// Everything a script can get wrong in its meta fails here, before a box or a model is spent on it.
func (s *Service) WorkflowRun(ctx context.Context, caller contract.Principal, req contract.WorkflowRunRequest) (contract.WorkflowRunResponse, *contract.Error) {
	var response contract.WorkflowRunResponse
	roomID, problem := s.workflowRoom(ctx, caller, req.Room)
	if problem != nil {
		return response, problem
	}
	effective, err := s.store.WorkflowEffective(ctx, roomID)
	if err != nil {
		return response, failure(err)
	}
	if problem := s.workflowStarters(ctx, caller, roomID, effective); problem != nil {
		return response, problem
	}
	if req.Resume != "" && !effective.Resume {
		return response, workflowRefusal(contract.WorkflowSetting(contract.WorkflowFeatureResume), "starting a run with resume is turned off in this room")
	}
	if len(req.ClientID) > 64 {
		return response, invalid("clientId is longer than 64 characters")
	}
	if req.Args != "" && !json.Valid([]byte(req.Args)) {
		return response, invalid("args is not JSON")
	}
	meta, repo, problem := s.workflowScript(ctx, roomID, req.Script)
	if problem != nil {
		return response, problem
	}
	if problem := workflowProfiles(effective, meta.Profiles); problem != nil {
		return response, problem
	}
	if req.Resume != "" {
		resumed, err := s.store.WorkflowRun(ctx, req.Resume)
		if err != nil {
			return response, failure(err)
		}
		if resumed.Room != roomID {
			return response, forbidden()
		}
		if resumed.State == contract.WorkflowRunning {
			return response, &contract.Error{Code: contract.ErrConflict, Message: "that run is still going; it resumes by itself after a box loss, and reload changes its script"}
		}
		if resumed.Repo != repo {
			return response, invalid("a resumed run stays on its repository, " + string(resumed.Repo))
		}
	}
	runID, err := id("wf_", s.now())
	if err != nil {
		return response, failure(err)
	}
	name := meta.Name + "-" + strings.ToLower(runID[len(runID)-6:])
	if !mentionName.MatchString(name) {
		return response, invalid("meta.name must be letters, digits, dots, underscores or hyphens, at most 57 of them, and start with a letter or digit")
	}
	run := store.WorkflowRun{ID: contract.WorkflowRunID(runID), Room: roomID, Name: meta.Name, Description: meta.Description, Script: req.Script, Args: req.Args, Profiles: meta.Profiles, Repo: repo, Resume: req.Resume, MaxRuns: effective.MaxRuns}
	started, problem := s.startSession(ctx, caller, contract.SessionStartRequest{Room: roomID, Name: name, Profile: meta.Profiles[0], Repo: repo, Size: cmp.Or(meta.Size, effective.Size), GPU: meta.GPU, Brief: "Run workflow " + meta.Name + ".", ClientID: req.ClientID}, "", &run)
	if problem != nil {
		return response, problem
	}
	// A retried client ID returns the first run.
	stored, err := s.store.SessionWorkflow(ctx, started.Session)
	if err != nil {
		return response, failure(err)
	}
	return contract.WorkflowRunResponse{Run: stored.ID, Session: started.Session, Box: started.Box}, nil
}

// WorkflowReload gives a running run a new script, which its crew runs in place of the old one in the same box, on the same
// branches and journal. The meta must keep the run's name, repo and size, which its session and box were made for.
func (s *Service) WorkflowReload(ctx context.Context, caller contract.Principal, req contract.WorkflowReloadRequest) (contract.WorkflowReloadResponse, *contract.Error) {
	var response contract.WorkflowReloadResponse
	run, err := s.store.WorkflowRun(ctx, req.Run)
	if err != nil {
		return response, failure(err)
	}
	if problem := s.roomAccess(ctx, caller, run.Room); problem != nil {
		return response, problem
	}
	tree, err := s.store.RoomTree(ctx, run.Room)
	if err != nil {
		return response, failure(err)
	}
	if !tree.CanManage(caller, run.Session) {
		return response, &contract.Error{Code: contract.ErrForbidden, Message: "only the owner, the session that started a workflow or a session above it reloads it"}
	}
	ended := &contract.Error{Code: contract.ErrConflict, Message: "run " + string(run.ID) + " has ended; reload is for a running run, and run with resume starts a new one from an ended run"}
	if run.State != contract.WorkflowRunning {
		return response, ended
	}
	meta, repo, problem := s.workflowScript(ctx, run.Room, req.Script)
	if problem != nil {
		return response, problem
	}
	effective, err := s.store.WorkflowEffective(ctx, run.Room)
	if err != nil {
		return response, failure(err)
	}
	if problem := workflowProfiles(effective, meta.Profiles); problem != nil {
		return response, problem
	}
	current, err := parseWorkflowMeta(run.Script)
	if err != nil {
		return response, failure(err)
	}
	for _, field := range []struct{ name, now, was string }{{"name", meta.Name, run.Name}, {"repo", string(repo), string(run.Repo)}, {"size", string(meta.Size), string(current.Size)}, {"gpu", strconv.Itoa(int(meta.GPU)), strconv.Itoa(int(current.GPU))}} {
		if field.now != field.was {
			return response, invalid(fmt.Sprintf("a reload keeps the run's meta.%s, which is %s, and this script has %s; a different %s needs a new run", field.name, metaText(field.was), metaText(field.now), field.name))
		}
	}
	version, err := s.store.ReloadWorkflow(ctx, run.ID, req.Script, meta.Description, meta.Profiles, s.now())
	if errors.Is(err, store.ErrWorkflowEnded) {
		return response, ended
	}
	if err != nil {
		return response, failure(err)
	}
	return contract.WorkflowReloadResponse{Run: run.ID, Version: version}, nil
}

// metaText quotes a meta value for a message, which says so when the script left it out.
func metaText(value string) string {
	if value == "" {
		return "unset"
	}
	return fmt.Sprintf("%q", value)
}

func (s *Service) workflowInfo(ctx context.Context, run store.WorkflowRun) (contract.WorkflowInfo, error) {
	summary, err := s.store.WorkflowSummary(ctx, run.ID)
	if err != nil {
		return contract.WorkflowInfo{}, err
	}
	_, running, _, err := s.store.WorkflowCounts(ctx, run.ID)
	if err != nil {
		return contract.WorkflowInfo{}, err
	}
	recorded, err := s.store.SessionBranches(ctx, run.Repo, run.Session)
	info := contract.WorkflowInfo{Run: run.ID, Room: run.Room, Session: run.Session, Name: run.Name, Description: run.Description, Profiles: run.Profiles, Repo: run.Repo, Commit: run.Commit, Version: run.Version, State: run.State, Result: run.Result, Steps: summary.Steps, Running: running, Failed: summary.Failed, FailedSteps: summary.FailedSteps, Usage: summary.Usage, Started: run.Started, Ended: run.Ended}
	if len(recorded) > 0 {
		info.Branch = recorded[0]
	}
	if n := len(run.StatusLines); n > 0 {
		info.Status, info.StatusAt = harness.Mask(run.StatusLines[n-1].Text), run.StatusLines[n-1].At
	}
	return info, err
}

// statusLineRunes bounds a status() line: one line that a run's header and the Runs list show whole or nearly.
const statusLineRunes = 200

// statusLine is a script's status() text as one line.
func statusLine(text string) string {
	line := strings.Join(strings.Fields(text), " ")
	if runes := []rune(line); len(runes) > statusLineRunes {
		line = string(runes[:statusLineRunes-1]) + "…"
	}
	return line
}

func (s *Service) WorkflowStatus(ctx context.Context, caller contract.Principal, req contract.WorkflowStatusRequest) (contract.WorkflowStatusResponse, *contract.Error) {
	var run store.WorkflowRun
	var err error
	if req.Run != "" {
		run, err = s.store.WorkflowRun(ctx, req.Run)
	} else {
		run, err = s.store.SessionWorkflow(ctx, req.Session)
	}
	if err != nil {
		return contract.WorkflowStatusResponse{}, failure(err)
	}
	if problem := s.roomAccess(ctx, caller, run.Room); problem != nil {
		return contract.WorkflowStatusResponse{}, problem
	}
	info, err := s.workflowInfo(ctx, run)
	if err != nil {
		return contract.WorkflowStatusResponse{}, failure(err)
	}
	steps, err := s.store.WorkflowSteps(ctx, run.ID)
	if err != nil {
		return contract.WorkflowStatusResponse{}, failure(err)
	}
	merge, err := s.store.WorkflowMerge(ctx, run.ID)
	if err != nil {
		return contract.WorkflowStatusResponse{}, failure(err)
	}
	info.Result = harness.Mask(info.Result)
	for i := range steps {
		maskStep(&steps[i])
	}
	lines := make([]contract.WorkflowStatusLine, 0, len(run.StatusLines))
	for _, line := range run.StatusLines {
		lines = append(lines, contract.WorkflowStatusLine{Text: harness.Mask(line.Text), At: line.At})
	}
	return contract.WorkflowStatusResponse{Run: info, Steps: steps, Merge: merge, StatusLines: lines}, nil
}

// WorkflowStep reads what a journal row leaves out, for the run page. A closed crew keeps it.
func (s *Service) WorkflowStep(ctx context.Context, caller contract.Principal, req contract.WorkflowStepRequest) (contract.WorkflowStepResponse, *contract.Error) {
	run, err := s.store.SessionWorkflow(ctx, req.Session)
	if err != nil {
		return contract.WorkflowStepResponse{}, failure(err)
	}
	if problem := s.roomAccess(ctx, caller, run.Room); problem != nil {
		return contract.WorkflowStepResponse{}, problem
	}
	detail, result, err := s.store.WorkflowStepOutput(ctx, run.ID, req.Key)
	if err != nil {
		return contract.WorkflowStepResponse{}, failure(err)
	}
	return contract.WorkflowStepResponse{Detail: harness.Mask(detail), Result: harness.Mask(result)}, nil
}

// maskStep covers what a read can show: a step's name quotes its command and its reason quotes the command's output.
func maskStep(step *contract.WorkflowStep) {
	step.Name, step.Reason = harness.Mask(step.Name), harness.Mask(step.Reason)
}

func (s *Service) WorkflowList(ctx context.Context, caller contract.Principal, req contract.WorkflowListRequest) (contract.WorkflowListResponse, *contract.Error) {
	roomID, problem := s.workflowRoom(ctx, caller, req.Room)
	if problem != nil {
		return contract.WorkflowListResponse{}, problem
	}
	runs, err := s.store.WorkflowRuns(ctx, roomID)
	if err != nil {
		return contract.WorkflowListResponse{}, failure(err)
	}
	response := contract.WorkflowListResponse{Runs: make([]contract.WorkflowInfo, 0, len(runs))}
	for _, run := range runs {
		info, err := s.workflowInfo(ctx, run)
		if err != nil {
			return contract.WorkflowListResponse{}, failure(err)
		}
		response.Runs = append(response.Runs, info)
	}
	return response, nil
}

// callerWorkflow finds the run the calling box hosts. The box's identity is the journal's only key.
func (s *Service) callerWorkflow(ctx context.Context, caller contract.Principal) (store.WorkflowRun, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return store.WorkflowRun{}, forbidden()
	}
	run, err := s.store.SessionWorkflow(ctx, caller.Session)
	if errors.Is(err, store.ErrNotFound) {
		return run, &contract.Error{Code: contract.ErrForbidden, Message: "this session is not a crew"}
	}
	if err != nil {
		return run, failure(err)
	}
	return run, nil
}

func (s *Service) WorkflowPin(ctx context.Context, caller contract.Principal, req contract.WorkflowPinRequest) (contract.WorkflowPinResponse, *contract.Error) {
	run, problem := s.callerWorkflow(ctx, caller)
	if problem != nil {
		return contract.WorkflowPinResponse{}, problem
	}
	if err := s.store.PinWorkflowCommit(ctx, run.ID, req.Commit); err != nil {
		return contract.WorkflowPinResponse{}, failure(err)
	}
	return contract.WorkflowPinResponse{}, nil
}

func (s *Service) WorkflowStepStart(ctx context.Context, caller contract.Principal, req contract.WorkflowStepStartRequest) (contract.WorkflowStepStartResponse, *contract.Error) {
	run, problem := s.callerWorkflow(ctx, caller)
	if problem != nil {
		return contract.WorkflowStepStartResponse{}, problem
	}
	switch req.Kind {
	case contract.WorkflowStepAgent, contract.WorkflowStepCommand, contract.WorkflowStepPost, contract.WorkflowStepWorkflow, contract.WorkflowStepLog:
	default:
		return contract.WorkflowStepStartResponse{}, invalid("invalid step kind")
	}
	if req.Key == "" || run.State != contract.WorkflowRunning {
		return contract.WorkflowStepStartResponse{}, invalid("no running run for this step")
	}
	if req.Feature != "" {
		if !slices.Contains(contract.AllWorkflowFeatures, req.Feature) || req.Feature == contract.WorkflowFeatureResume {
			return contract.WorkflowStepStartResponse{}, invalid("invalid step feature")
		}
		effective, err := s.store.WorkflowEffective(ctx, run.Room)
		if err != nil {
			return contract.WorkflowStepStartResponse{}, failure(err)
		}
		if !effective.Allows(req.Feature) {
			return contract.WorkflowStepStartResponse{}, workflowRefusal(contract.WorkflowSetting(req.Feature), string(req.Feature)+" is turned off in this room's workflows")
		}
	}
	stored, err := s.store.StartWorkflowStep(ctx, run.ID, req, s.now())
	if err != nil {
		return contract.WorkflowStepStartResponse{}, failure(err)
	}
	if stored.State == "" {
		s.publishStep(ctx, run, req.Key)
	}
	return stored, nil
}

func (s *Service) WorkflowStepEnd(ctx context.Context, caller contract.Principal, req contract.WorkflowStepEndRequest) (contract.WorkflowStepEndResponse, *contract.Error) {
	run, problem := s.callerWorkflow(ctx, caller)
	if problem != nil {
		return contract.WorkflowStepEndResponse{}, problem
	}
	if req.State != contract.WorkflowStepDone && req.State != contract.WorkflowStepFailed {
		return contract.WorkflowStepEndResponse{}, invalid("invalid step state")
	}
	if err := s.store.EndWorkflowStep(ctx, run.ID, req, s.now()); err != nil {
		return contract.WorkflowStepEndResponse{}, failure(err)
	}
	s.publishStep(ctx, run, req.Key)
	return contract.WorkflowStepEndResponse{}, nil
}

func (s *Service) WorkflowStepTranscript(ctx context.Context, caller contract.Principal, req contract.WorkflowStepTranscriptRequest) (contract.WorkflowStepTranscriptResponse, *contract.Error) {
	run, problem := s.callerWorkflow(ctx, caller)
	if problem != nil {
		return contract.WorkflowStepTranscriptResponse{}, problem
	}
	if !filepath.IsLocal(req.Transcript) {
		return contract.WorkflowStepTranscriptResponse{}, invalid("transcript path must be local")
	}
	if err := s.store.SetWorkflowStepTranscript(ctx, run.ID, req.Key, req.Transcript); err != nil {
		return contract.WorkflowStepTranscriptResponse{}, failure(err)
	}
	s.publishStep(ctx, run, req.Key)
	return contract.WorkflowStepTranscriptResponse{}, nil
}

// WorkflowSetStatus keeps a script's status() line on its run and tells open pages. An empty line clears it, so the page
// counts the steps again.
func (s *Service) WorkflowSetStatus(ctx context.Context, caller contract.Principal, req contract.WorkflowSetStatusRequest) (contract.WorkflowSetStatusResponse, *contract.Error) {
	run, problem := s.callerWorkflow(ctx, caller)
	if problem != nil {
		return contract.WorkflowSetStatusResponse{}, problem
	}
	if run.State != contract.WorkflowRunning {
		return contract.WorkflowSetStatusResponse{}, invalid("no running run for this line")
	}
	text := statusLine(req.Text)
	if err := s.store.SetWorkflowStatus(ctx, run.ID, text, s.now()); err != nil {
		return contract.WorkflowSetStatusResponse{}, failure(err)
	}
	event, err := s.event(run.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.WorkflowStatusChanged{Run: run.ID, Session: run.Session, Status: harness.Mask(text)})
	if err != nil {
		log.Printf("workflow %s status: %v", run.ID, err)
		return contract.WorkflowSetStatusResponse{}, nil
	}
	s.live.Publish(run.Room, event, false)
	return contract.WorkflowSetStatusResponse{}, nil
}

// publishStep tells open run pages about a step, and the Runs list about the run's counts. The journal already holds it, so a
// failure here costs a page one update.
func (s *Service) publishStep(ctx context.Context, run store.WorkflowRun, key string) {
	step, err := s.store.WorkflowStep(ctx, run.ID, key)
	if err != nil {
		log.Printf("workflow %s step %s: %v", run.ID, key, err)
		return
	}
	steps, running, failed, err := s.store.WorkflowCounts(ctx, run.ID)
	if err != nil {
		log.Printf("workflow %s step %s: %v", run.ID, key, err)
		return
	}
	maskStep(&step)
	event, err := s.event(run.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.WorkflowStepChanged{Run: run.ID, Session: run.Session, Step: step, Steps: steps, Running: running, Failed: failed})
	if err != nil {
		log.Printf("workflow %s step %s: %v", run.ID, key, err)
		return
	}
	s.live.Publish(run.Room, event, false)
}

// boxWorkflow is what a hello tells a crew's box. A profile removed since the run started is left out, so a call to it fails.
// The room's workflow settings narrow its workers and set its default timeout.
func (s *Service) boxWorkflow(ctx context.Context, run store.WorkflowRun, memory int64) (*contract.BoxWorkflow, error) {
	effective, err := s.store.WorkflowEffective(ctx, run.Room)
	if err != nil {
		return nil, err
	}
	workers := int(max(memory/workerMemory, 1))
	if effective.MaxWorkers > 0 {
		workers = min(workers, effective.MaxWorkers)
	}
	summaries := s.profiles.List()
	workflow := &contract.BoxWorkflow{Run: run.ID, Name: run.Name, Script: run.Script, Version: run.Version, Args: run.Args, Workers: workers, Timeout: effective.Timeout}
	for _, name := range run.Profiles {
		model, _, err := s.profiles.Model(name)
		if err != nil {
			continue
		}
		profile := contract.WorkflowProfile{Name: name, Model: model, CompactTokens: s.profiles.CompactionTokens(name)}
		for _, summary := range summaries {
			if summary.Name == name {
				profile.MaxConcurrent = summary.MaxConcurrent
			}
		}
		workflow.Profiles = append(workflow.Profiles, profile)
	}
	return workflow, nil
}

// workflowSummary closes a run's journal, failing what its script left running, and totals it.
func (s *Service) workflowSummary(ctx context.Context, run store.WorkflowRun, state contract.WorkflowState) (contract.WorkflowEnded, error) {
	if err := s.store.AbandonWorkflowSteps(ctx, run.ID, s.now()); err != nil {
		return contract.WorkflowEnded{}, err
	}
	summary, err := s.store.WorkflowSummary(ctx, run.ID)
	summary.Session, summary.Name, summary.State = run.Session, run.Name, state
	return summary, err
}

// workflowOutput fills a crew's turn from its journal. The result stays as the script gave it, and the reply also names what
// failed, because the run's end is how the caller hears of it.
func (s *Service) workflowOutput(ctx context.Context, run store.WorkflowRun, req *contract.TurnOutputRequest) (contract.WorkflowEnded, error) {
	state := contract.WorkflowDone
	switch req.Outcome {
	case contract.TurnFailed:
		state = contract.WorkflowFailed
	case contract.TurnStopped:
		state = contract.WorkflowStopped
	}
	summary, err := s.workflowSummary(ctx, run, state)
	if err != nil {
		return summary, err
	}
	summary.Usage.Model = req.Model
	req.Usage = summary.Usage
	if req.Cause == contract.CauseNotStarted {
		req.Cause = "" // A stop before the script began still ends the run, so its input has no later turn to wait for.
	}
	if state == contract.WorkflowDone && len(req.Data) > 0 {
		data, files, err := s.preparePostData(ctx, run.Room, req.Data, nil)
		if err != nil {
			return summary, err
		}
		req.Data, req.Attachments = data, files
		req.Final = "Workflow " + run.Name + " finished."
	}
	if state != contract.WorkflowDone {
		req.Data, req.Attachments = nil, nil
	}
	failed := ""
	if summary.Failed > 0 {
		failed = fmt.Sprintf("\n\nFailed steps (%d): %s", summary.Failed, strings.Join(summary.FailedSteps, ", "))
	}
	switch {
	case state == contract.WorkflowFailed:
		req.Error = "workflow " + run.Name + ": " + req.Error + failed
	case state == contract.WorkflowStopped:
		req.Final = "Workflow " + run.Name + " stopped." + failed
	case req.Final == "":
		req.Final = "Workflow " + run.Name + " finished." + failed
	default:
		req.Final += failed
	}
	return summary, nil
}

// endsWorkflow says a turn ended the crew's run. A box that could not set up is a failed session like any other:
// session.failed tells the parent, and the run waits for a restart.
func endsWorkflow(req contract.TurnOutputRequest) bool {
	return req.Cause == "" || req.Cause == contract.CauseWorkflowFailed || req.Cause == contract.CauseNotStarted
}

// stopWorkflows ends the runs of sessions that are closing for any other reason, so no run stays running with no box.
func (s *Service) stopWorkflows(ctx context.Context, sessions []contract.SessionID) error {
	for _, id := range sessions {
		run, err := s.store.SessionWorkflow(ctx, id)
		if errors.Is(err, store.ErrNotFound) || err == nil && run.State != contract.WorkflowRunning {
			continue
		}
		if err != nil {
			return err
		}
		summary, err := s.workflowSummary(ctx, run, contract.WorkflowStopped)
		if err != nil {
			return err
		}
		event, err := s.event(run.Room, contract.Principal{Kind: contract.PrincipalEngine}, summary)
		if err != nil {
			return err
		}
		if err := s.store.EndWorkflow(ctx, run.ID, contract.WorkflowStopped, "", event); err != nil {
			return err
		}
	}
	return nil
}
