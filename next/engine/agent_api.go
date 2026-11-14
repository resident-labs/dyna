package engine

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

var (
	// agentTimeout is how long a call waits for an in-box agent's answer. Variables, so that a test need not wait them out.
	agentTimeout = 15 * time.Minute
	// agentStopWait is how long a call waits for the text of an agent it stopped.
	agentStopWait = 10 * time.Second
	// agentBoxCheck is how often a call looks whether the box the agent runs in is still there.
	agentBoxCheck = 2 * time.Second
)

// agentSpec is one turn of an in-box agent. Its tools are the read-only list: the engine asks for no other.
type agentSpec struct {
	ID           contract.AgentID
	Name         string
	Profile      contract.WorkflowProfile
	Instructions string
	Persist      bool
	Orient       bool
	FreshPrompt  string
	Prompt       string
}

// agentCause says why a call stopped waiting before the agent finished, or is empty when it finished.
type agentCause string

const (
	agentOwnerStopped agentCause = "owner"     // the owner pressed Stop
	agentCancelled    agentCause = "cancelled" // the caller's own turn was stopped
	agentTimedOut     agentCause = "timeout"
	agentBoxGone      agentCause = "box" // the box went away, and the agent with it
)

// agentResult is what the agent said, or the text it had when it was stopped.
type agentResult struct {
	contract.AgentEndedRequest
	Cause agentCause
}

// agentRun is a call waiting for its agent.
type agentRun struct {
	session contract.SessionID
	ended   chan contract.AgentEndedRequest
	stop    chan struct{} // closed by the owner's stop
	once    sync.Once
}

// agentRuns are the calls waiting, by agent ID.
type agentRuns struct {
	mu   sync.Mutex
	runs map[contract.AgentID]*agentRun
}

func (r *agentRuns) add(id contract.AgentID, run *agentRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = make(map[contract.AgentID]*agentRun)
	}
	r.runs[id] = run
}

func (r *agentRuns) get(id contract.AgentID) *agentRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id]
}

func (r *agentRuns) remove(id contract.AgentID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, id)
}

// sendAgent queues a message for the session's box. It reports false when the box has no channel or it does not take it.
func (s *Service) sendAgent(session contract.SessionID, body contract.BoxMessageBody) bool {
	handle, ok := s.live.LookupSession(session)
	if !ok || !handle.Lock() {
		return false
	}
	defer handle.Unlock()
	channel := handle.Channel()
	return channel != nil && channel.SendAgent(body)
}

// askAgent runs one turn of an in-box agent in the session's box, beside the session's running turn, and waits for its answer. It
// ends early on ctx, on the timeout, on the owner's stop and when the box goes away; it stops the agent unless the box is gone, and
// returns the text the agent had, which a stopped agent still sends.
func (s *Service) askAgent(ctx context.Context, session store.Session, spec agentSpec) (agentResult, error) {
	if session.Turn == "" {
		return agentResult{}, errors.New("the session has no running turn to file the agent under")
	}
	delivery, err := s.store.TurnDelivery(ctx, session.ID, session.Turn)
	if err != nil {
		return agentResult{}, err
	}
	run := &agentRun{session: session.ID, ended: make(chan contract.AgentEndedRequest, 1), stop: make(chan struct{})}
	s.agents.add(spec.ID, run)
	defer s.agents.remove(spec.ID)
	if err := s.store.StartAgent(ctx, store.Agent{ID: spec.ID, Session: session.ID, Turn: session.Turn, Name: spec.Name, Started: s.now()}); err != nil {
		return agentResult{}, err
	}
	finish := func(ended contract.AgentEndedRequest, cause agentCause) (agentResult, error) {
		// Whatever ended the wait, the record ends too. A turn the box answered has ended already, and keeps its own end.
		if err := s.store.EndAgent(context.WithoutCancel(ctx), session.ID, spec.ID, ended.Outcome, ended.Usage, s.now()); err != nil {
			return agentResult{}, err
		}
		return agentResult{AgentEndedRequest: ended, Cause: cause}, nil
	}
	turn := contract.AgentTurn{ID: spec.ID, Name: spec.Name, Profile: spec.Profile, Tools: contract.AgentReadOnlyTools, Instructions: spec.Instructions, Persist: spec.Persist, Prompt: spec.Prompt, FreshPrompt: spec.FreshPrompt, Orient: spec.Orient, Turn: session.Turn, Delivery: delivery}
	if !s.sendAgent(session.ID, turn) {
		_, err := finish(contract.AgentEndedRequest{ID: spec.ID, Outcome: contract.TurnFailed}, "")
		return agentResult{}, errors.Join(errors.New("the session's box is not connected"), err)
	}
	timeout := time.NewTimer(agentTimeout)
	defer timeout.Stop()
	check := time.NewTicker(agentBoxCheck)
	defer check.Stop()
	gone := 0
	var cause agentCause
	for cause == "" {
		select {
		case ended := <-run.ended:
			return finish(ended, "")
		case <-ctx.Done():
			cause = agentCancelled
		case <-run.stop:
			cause = agentOwnerStopped
		case <-timeout.C:
			cause = agentTimedOut
		case <-check.C:
			if box, err := s.store.Box(ctx, session.Box); err == nil && box.State != contract.BoxReady {
				gone++
			} else {
				gone = 0
			}
			if gone == 3 {
				cause = agentBoxGone
			}
		}
	}
	if cause != agentBoxGone && s.sendAgent(session.ID, contract.AgentStop{ID: spec.ID}) {
		select {
		case ended := <-run.ended:
			return finish(ended, cause)
		case <-time.After(agentStopWait):
		}
	}
	return finish(contract.AgentEndedRequest{ID: spec.ID, Outcome: contract.TurnStopped}, cause)
}

// agentOf finds the agent turn a box calls about, which must be its own.
func (s *Service) agentOf(ctx context.Context, caller contract.Principal, id contract.AgentID) (store.Agent, store.Session, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return store.Agent{}, store.Session{}, forbidden()
	}
	agent, err := s.store.Agent(ctx, id)
	if errors.Is(err, store.ErrNotFound) || err == nil && agent.Session != caller.Session {
		return agent, store.Session{}, &contract.Error{Code: contract.ErrNotFound, Message: "agent not found"}
	}
	if err != nil {
		return agent, store.Session{}, failure(err)
	}
	session, problem := s.callerSession(ctx, caller)
	return agent, session, problem
}

// agentChanged tells the pages reading the parent turn that its agent's steps changed.
func (s *Service) agentChanged(caller contract.Principal, session store.Session, agent store.Agent) {
	if event, err := s.event(session.Room, caller, contract.TranscriptChanged{Session: session.ID, Turn: agent.Turn}); err == nil {
		s.live.Publish(session.Room, event, true)
	}
}

// AgentStarted records where the agent's turn lies in the session's transcript, once its box has opened it.
func (s *Service) AgentStarted(ctx context.Context, caller contract.Principal, req contract.AgentStartedRequest) (contract.AgentStartedResponse, *contract.Error) {
	agent, session, problem := s.agentOf(ctx, caller, req.ID)
	if problem != nil {
		return contract.AgentStartedResponse{}, problem
	}
	if req.Offset < 0 || !filepath.IsLocal(req.Transcript) {
		return contract.AgentStartedResponse{}, invalid("transcript path must be local and offset must not be negative")
	}
	if err := s.store.AgentFile(ctx, caller.Session, req.ID, req.Transcript, req.Offset); err != nil {
		return contract.AgentStartedResponse{}, failure(err)
	}
	s.agentChanged(caller, session, agent)
	return contract.AgentStartedResponse{}, nil
}

// AgentEnded is the box's answer for an agent turn. It reaches the call waiting for it; a call that gave up has no one to reach.
func (s *Service) AgentEnded(ctx context.Context, caller contract.Principal, req contract.AgentEndedRequest) (contract.AgentEndedResponse, *contract.Error) {
	agent, session, problem := s.agentOf(ctx, caller, req.ID)
	if problem != nil {
		return contract.AgentEndedResponse{}, problem
	}
	if err := s.store.EndAgent(ctx, caller.Session, req.ID, req.Outcome, req.Usage, s.now()); err != nil {
		return contract.AgentEndedResponse{}, failure(err)
	}
	if run := s.agents.get(req.ID); run != nil {
		select {
		case run.ended <- req:
		default:
		}
	}
	s.agentChanged(caller, session, agent)
	return contract.AgentEndedResponse{}, nil
}

// AgentStop is the owner's stop of an agent that runs for a session. The call that asked for it returns the text so far.
func (s *Service) AgentStop(_ context.Context, _ contract.Principal, req contract.AgentStopRequest) (contract.AgentStopResponse, *contract.Error) {
	run := s.agents.get(req.ID)
	if run == nil || run.session != req.Session {
		return contract.AgentStopResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "that agent is not running"}
	}
	run.once.Do(func() { close(run.stop) })
	return contract.AgentStopResponse{}, nil
}
