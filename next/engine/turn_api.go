package engine

import (
	"context"
	"errors"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) BoxHello(ctx context.Context, caller contract.Principal, req contract.BoxHelloRequest) (contract.BoxHelloResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.BoxHelloResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	// This goes before the box is ready, or a mark from its last run could stop it again.
	if handle, ok := s.lookupSession(session.ID); ok {
		handle.ClearRefresh()
		handle.Unlock()
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	if req.WaitForChannel && (current.State == contract.BoxCreating || current.State == contract.BoxStarting) {
		event, eventErr := s.engineEvent(session.Room, contract.BoxChanged{})
		if eventErr != nil {
			return contract.BoxHelloResponse{}, failure(eventErr)
		}
		if err := s.store.BeginSessionStartup(ctx, current, event); err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
	} else if current.State == contract.BoxCreating || current.State == contract.BoxStarting || current.State == contract.BoxLost {
		_, err = s.transition(ctx, session.Room, current, contract.BoxReady, "", "")
		if errors.Is(err, store.ErrNotFound) && current.State == contract.BoxCreating {
			current, err = s.store.Box(ctx, session.Box)
			if err == nil && current.State == contract.BoxStarting {
				_, err = s.transition(ctx, session.Room, current, contract.BoxReady, "", "")
			} else if err == nil && current.State != contract.BoxReady {
				err = store.ErrNotFound
			}
		}
		if err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
	} else if current.State != contract.BoxReady && current.State != contract.BoxClosing {
		return contract.BoxHelloResponse{}, invalid("box is not starting, ready, or closing")
	}
	if err := s.store.ForgetShellJobs(ctx, session.ID); err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	model, _, err := s.profiles.Model(session.Profile)
	if err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	var repos []contract.BoxRepo
	if s.repos != nil {
		current, err := s.store.Box(ctx, session.Box)
		if err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
		// A session's first setup cuts its branch from the cache, so the cache is made current now, not at box creation.
		hadTurns, err := s.store.SessionHasTurns(ctx, session.ID)
		if err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
		if hadTurns {
			repos, err = s.repos.Get(ctx, session.ID, current.Host, false)
		} else {
			var stale string
			repos, stale, err = s.repos.Seed(ctx, session.ID, current.Host)
			if err == nil && stale != "" {
				var event contract.Event
				if event, err = s.engineEvent(session.Room, contract.SessionNotice{Session: session.ID, Text: stale, ClientID: "seed-stale:" + string(session.Box)}); err == nil {
					err = s.store.SendSessionNotice(ctx, event, session.ID)
				}
			}
		}
		if err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
	}
	files, err := s.store.Transcripts(ctx, session.ID)
	if err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	roomName, parentName, err := s.store.SessionContextNames(ctx, session.ID)
	if err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	vars := promptVars{Role: roleWorker, Name: session.Name, ID: session.ID, Room: roomName, RoomID: session.Room, Parent: parentName, CPUs: current.Size.CPUs, MemBytes: current.Size.MemBytes}
	if len(repos) > 0 {
		vars.Repo = &repos[0]
	}
	if parentName == "" {
		vars.Role = roleRoot
		hosts, err := s.store.Hosts(ctx)
		if err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
		vars.Host = "the engine host"
		for _, h := range hosts {
			if h.EngineHost != nil && *h.EngineHost {
				vars.Host = string(h.Name)
				break
			}
		}
		vars.Shared, vars.GlobalRules, vars.RoomRules = s.layout.Shared(session.Room), s.layout.Instructions(""), s.layout.Instructions(session.Room)
	} else {
		hasChildren, err := s.store.SessionHasChildren(ctx, session.ID)
		if err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
		if hasChildren {
			vars.Role = roleLead
		}
	}
	var workflow *contract.BoxWorkflow
	run, err := s.store.SessionWorkflow(ctx, session.ID)
	if err == nil {
		workflow, err = s.boxWorkflow(ctx, run, current.Size.MemBytes)
		if err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
		for i := range repos {
			repos[i].Commit = run.Commit
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return contract.BoxHelloResponse{}, failure(err)
	}
	return contract.BoxHelloResponse{Room: session.Room, GitName: s.gitName, GitEmail: s.gitEmail, Session: session.ID, Box: session.Box, Model: model, ModelInstructions: modelInstructions(model), TextOnlyModels: s.profiles.TextOnlyModels(), CompactTokens: s.profiles.CompactionTokens(session.Profile), Repos: repos, Transcripts: files, Instructions: sessionPrompt(vars), WorkerInstructions: promptCore, Root: vars.Role == roleRoot, Workflow: workflow, GitHub: s.githubTools != nil}, nil
}

func (s *Service) DeliveryDone(ctx context.Context, caller contract.Principal, req contract.DeliveryDoneRequest) (contract.DeliveryDoneResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.DeliveryDoneResponse{}, forbidden()
	}
	if err := s.store.DeliveryDone(ctx, caller.Session, req.Delivery); err != nil {
		return contract.DeliveryDoneResponse{}, failure(err)
	}
	return contract.DeliveryDoneResponse{}, nil
}

func (s *Service) TurnOutput(ctx context.Context, caller contract.Principal, req contract.TurnOutputRequest) (contract.TurnOutputResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.TurnOutputResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.TurnOutputResponse{}, failure(err)
	}
	if req.Outcome != contract.TurnCompleted && req.Outcome != contract.TurnFailed && req.Outcome != contract.TurnStopped {
		return contract.TurnOutputResponse{}, invalid("invalid turn outcome")
	}
	if req.Cause == contract.CauseNotStarted {
		if req.Outcome != contract.TurnStopped {
			return contract.TurnOutputResponse{}, invalid("not_started needs a stopped outcome")
		}
	} else if req.Cause != "" && req.Outcome != contract.TurnFailed {
		return contract.TurnOutputResponse{}, invalid("turn cause needs a failed outcome")
	}
	switch req.Cause {
	case "", contract.CauseSetupFailed, contract.CauseModelFailed, contract.CauseOutputLimit, contract.CauseHarnessFailed, contract.CauseSetModelFailed, contract.CauseNotStarted, contract.CauseWorkflowFailed:
	default:
		return contract.TurnOutputResponse{}, invalid("invalid turn cause")
	}
	// Close stops the agent program under a running turn, so that turn reports the program's end. The session is closing, so
	// the turn was ended by the close and did not fail.
	if session.Closing && req.Outcome == contract.TurnFailed && req.Cause == contract.CauseHarnessFailed {
		req.Outcome, req.Cause, req.Error = contract.TurnStopped, contract.CauseClosed, ""
	}
	req.Usage.Model = req.Model
	run, err := s.store.SessionWorkflow(ctx, session.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return contract.TurnOutputResponse{}, failure(err)
	}
	crew := err == nil && endsWorkflow(req)
	if !crew && len(req.Data) > 0 {
		return contract.TurnOutputResponse{}, invalid("structured result data is only for a workflow")
	}
	result := req.Final
	if len(req.Data) > 0 {
		result = string(req.Data)
	}
	if req.Outcome == contract.TurnFailed {
		result = req.Error // A run that threw has no value, and the journal must say why once its box is gone.
	}
	var summary contract.WorkflowEnded
	if crew {
		if summary, err = s.workflowOutput(ctx, run, &req); err != nil {
			return contract.TurnOutputResponse{}, failure(err)
		}
	}
	event, err := s.event(session.Room, caller, contract.TurnEnded{Session: session.ID, Turn: req.Turn, Outcome: req.Outcome, Final: req.Final, Data: req.Data, DataAttachment: req.DataAttachment, Attachments: req.Attachments, Usage: req.Usage, Error: req.Error, Cause: req.Cause, ShellJobs: req.ShellJobs, Jobs: req.Jobs})
	if err != nil {
		return contract.TurnOutputResponse{}, failure(err)
	}
	if crew && run.State == contract.WorkflowRunning {
		var ended contract.Event
		if ended, err = s.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, summary); err == nil {
			err = s.store.EndWorkflowTurn(ctx, session.ID, req, event, run.ID, summary.State, result, ended)
		}
	} else {
		err = s.store.EndTurn(ctx, session.ID, req, event)
	}
	if err != nil {
		return contract.TurnOutputResponse{}, failure(err)
	}
	if crew {
		// Closing saves a diff of what the workers left unpushed. A close that fails here leaves a quiet box, which the dormant
		// sweep closes, and the run's result is already safe.
		if _, problem := s.closeSession(ctx, session, false, false); problem != nil {
			log.Printf("workflow %s close: %s", run.ID, problem.Message)
		}
	}
	return contract.TurnOutputResponse{}, nil
}

func (s *Service) TurnActivity(ctx context.Context, caller contract.Principal, req contract.TurnActivityRequest) (contract.TurnActivityResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.TurnActivityResponse{}, forbidden()
	}
	if req.Kind == "" {
		req.Kind = contract.ActivityTool // a box agent that predates kinds reports tool calls only
	}
	if req.Kind != contract.ActivityTool && req.Kind != contract.ActivityCompaction && req.Kind != contract.ActivityModel && req.Kind != contract.ActivityModelWait {
		return contract.TurnActivityResponse{}, invalid("activity kind must be tool, compaction or model")
	}
	if !utf8.ValidString(req.Label) || strings.ContainsAny(req.Label, "\r\n") || utf8.RuneCountInString(req.Label) > 80 {
		return contract.TurnActivityResponse{}, invalid("activity label must be one line of at most 80 characters")
	}
	session, open, err := s.store.OpenSession(ctx, caller.Session)
	if err != nil {
		return contract.TurnActivityResponse{}, failure(err)
	}
	if !open {
		return contract.TurnActivityResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	body := contract.TurnActivity{Session: caller.Session, Turn: req.Turn, Kind: req.Kind, Label: req.Label}
	event, err := s.event(session.Room, caller, body)
	if err != nil {
		return contract.TurnActivityResponse{}, failure(err)
	}
	handle, problem := s.createSession(ctx, session.ID)
	if problem != nil {
		return contract.TurnActivityResponse{}, problem
	}
	if err := s.store.MarkTurnActive(ctx, session.ID, req.Turn, s.now()); err != nil {
		return contract.TurnActivityResponse{}, failure(err)
	}
	if !handle.PublishActivity(session.Room, body, event) {
		return contract.TurnActivityResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	return contract.TurnActivityResponse{}, nil
}

func (s *Service) ConversationRead(ctx context.Context, caller contract.Principal, req contract.ConversationReadRequest) (contract.ConversationReadResponse, *contract.Error) {
	if req.Seq < 0 || (req.Conversation.Kind != contract.ConversationFeed && req.Conversation.Kind != contract.ConversationDM) {
		return contract.ConversationReadResponse{}, invalid("invalid conversation or seq")
	}
	home := string(req.Conversation.Kind)
	if req.Conversation.Kind == contract.ConversationDM {
		session, err := s.store.Session(ctx, req.Conversation.Session)
		if err != nil {
			return contract.ConversationReadResponse{}, failure(err)
		}
		if session.Room != req.Room {
			return contract.ConversationReadResponse{}, invalid("session does not belong to room")
		}
		home += ":" + string(req.Conversation.Session)
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	seq, err := s.store.SetConversationRead(ctx, req.Room, home, req.Seq)
	if err != nil {
		return contract.ConversationReadResponse{}, failure(err)
	}
	event, err := s.event(req.Room, caller, contract.ConversationRead{Room: req.Room, Conversation: req.Conversation, Seq: seq})
	if err != nil {
		return contract.ConversationReadResponse{}, failure(err)
	}
	s.live.Publish(req.Room, event, false)
	return contract.ConversationReadResponse{}, nil
}
