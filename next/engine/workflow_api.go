package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
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

// WorkflowRun checks a script and starts its crew: one child session of the caller, in one box, whose only turn runs it.
// Everything a script can get wrong in its meta fails here, before a box or a model is spent on it.
func (s *Service) WorkflowRun(ctx context.Context, caller contract.Principal, req contract.WorkflowRunRequest) (contract.WorkflowRunResponse, *contract.Error) {
	var response contract.WorkflowRunResponse
	roomID, problem := s.workflowRoom(ctx, caller, req.Room)
	if problem != nil {
		return response, problem
	}
	tree, err := s.store.RoomTree(ctx, roomID)
	if err != nil {
		return response, failure(err)
	}
	if caller.Kind == contract.PrincipalSession && caller.Session != tree.Root() {
		return response, &contract.Error{Code: contract.ErrForbidden, Message: "only the owner or a room's root can start a workflow"}
	}
	if len(req.ClientID) > 64 {
		return response, invalid("clientId is longer than 64 characters")
	}
	if len(req.Script) > workflowScriptBytes {
		return response, invalid("the script is over 1 MiB")
	}
	if req.Args != "" && !json.Valid([]byte(req.Args)) {
		return response, invalid("args is not JSON")
	}
	meta, err := parseWorkflowMeta(req.Script)
	if err != nil {
		return response, invalid(err.Error())
	}
	for _, name := range meta.Profiles {
		if _, _, err := s.profiles.Model(name); err != nil {
			return response, invalid("unknown profile " + string(name))
		}
	}
	repos, err := s.store.RoomRepos(ctx, roomID)
	if err != nil {
		return response, failure(err)
	}
	repo, err := room.RepoForSession(repos, meta.Repo, "")
	if err != nil {
		return response, invalid(err.Error())
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
			return response, &contract.Error{Code: contract.ErrConflict, Message: "that run is still going; it resumes by itself after a box loss"}
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
	run := store.WorkflowRun{ID: contract.WorkflowRunID(runID), Room: roomID, Name: meta.Name, Description: meta.Description, Script: req.Script, Args: req.Args, Profiles: meta.Profiles, Repo: repo, Resume: req.Resume}
	started, problem := s.startSession(ctx, caller, contract.SessionStartRequest{Room: roomID, Name: name, Profile: meta.Profiles[0], Repo: repo, Size: meta.Size, Brief: "Run workflow " + meta.Name + ".", ClientID: req.ClientID}, "", &run)
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

func (s *Service) workflowInfo(ctx context.Context, run store.WorkflowRun) (contract.WorkflowInfo, error) {
	summary, err := s.store.WorkflowSummary(ctx, run.ID)
	return contract.WorkflowInfo{Run: run.ID, Room: run.Room, Session: run.Session, Name: run.Name, Description: run.Description, Profiles: run.Profiles, Repo: run.Repo, Commit: run.Commit, State: run.State, Result: run.Result, Failed: summary.Failed, FailedSteps: summary.FailedSteps, Usage: summary.Usage, Started: run.Started, Ended: run.Ended}, err
}

func (s *Service) WorkflowStatus(ctx context.Context, caller contract.Principal, req contract.WorkflowStatusRequest) (contract.WorkflowStatusResponse, *contract.Error) {
	run, err := s.store.WorkflowRun(ctx, req.Run)
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
	return contract.WorkflowStatusResponse{Run: info, Steps: steps}, nil
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
	stored, err := s.store.StartWorkflowStep(ctx, run.ID, req, s.now())
	if err != nil {
		return contract.WorkflowStepStartResponse{}, failure(err)
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
	return contract.WorkflowStepEndResponse{}, nil
}

// boxWorkflow is what a hello tells a crew's box. A profile removed since the run started is left out, so a call to it fails.
func (s *Service) boxWorkflow(run store.WorkflowRun, memory int64) *contract.BoxWorkflow {
	summaries := s.profiles.List()
	workflow := &contract.BoxWorkflow{Run: run.ID, Name: run.Name, Script: run.Script, Args: run.Args, Workers: int(max(memory/workerMemory, 1))}
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
	return workflow
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
