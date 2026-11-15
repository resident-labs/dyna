package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/engine/workflowmeta"
	"github.com/resident-labs/dyna/harness"
)

const (
	// A service waits this long to start again after its first, second and later failures in a row.
	serviceBackoffFirst  = time.Minute
	serviceBackoffSecond = 5 * time.Minute
	serviceBackoffLater  = 30 * time.Minute
	// handOverBatch is the most events one request hands a box.
	handOverBatch = 64
)

func serviceBackoff(failures int) time.Duration {
	switch failures {
	case 1:
		return serviceBackoffFirst
	case 2:
		return serviceBackoffSecond
	}
	return serviceBackoffLater
}

// callsServe finds the call a service's script must make. It also matches one in a comment, and the box refuses a script whose
// body ends without calling it, so this only spares a person the wait for the run to fail.
var callsServe = regexp.MustCompile(`\bserve\s*\(`)

func checkServes(script string) *contract.Error {
	if callsServe.MatchString(script) {
		return nil
	}
	return invalid("a workflow subscription runs as a service: call serve(async (event, key, state) => { … }) in the script to handle each event (events.md in the dyna-workflows skill)")
}

// serveSubscriptions gives each enabled workflow subscription its one run, and ends the run of one that is disabled or gone.
// A run that failed starts again after its backoff. A run the room has no place for yet waits for the next pass.
func (s *Service) serveSubscriptions(ctx context.Context) error {
	var failures []error
	unwanted, err := s.store.UnwantedServiceRuns(ctx)
	if err != nil {
		return err
	}
	for _, run := range unwanted {
		session, err := s.store.Session(ctx, run.Session)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if _, problem := s.closeSession(ctx, session, false, false); problem != nil {
			failures = append(failures, errors.New(problem.Message))
		}
	}
	unserved, err := s.store.UnservedSubscriptions(ctx, s.now())
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, sub := range unserved {
		failures = append(failures, s.startService(ctx, sub))
	}
	return errors.Join(failures...)
}

func (s *Service) startService(ctx context.Context, sub store.Subscription) error {
	parent := sub.Parent
	if parent != "" {
		target, err := s.store.Session(ctx, parent)
		if errors.Is(err, store.ErrNotFound) || err == nil && target.Closing {
			parent = ""
		} else if err != nil {
			return err
		}
	}
	if parent == "" {
		tree, err := s.store.RoomTree(ctx, sub.Room)
		if err != nil {
			return err
		}
		parent = tree.Root()
	}
	args, err := json.Marshal(sub.Workflow.Args)
	if err != nil {
		return err
	}
	_, problem := s.workflowRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent}, contract.WorkflowRunRequest{Room: sub.Room, Script: sub.Workflow.Script, Args: string(args)}, sub.ID)
	switch {
	case problem == nil:
		return nil
	case problem.Workflow != nil && problem.Workflow.Setting == contract.WorkflowSettingMaxRuns || problem.Code == contract.ErrUnavailable:
		return nil // no room for it yet
	}
	return s.failService(ctx, sub, parent, contract.CauseStartFailed, problem.Message)
}

// failService records a service run that did not start or ended in failure. It holds the next start for the backoff. The
// first failure of a streak also tells the parent, and the rest stay in hook.finished, so a service that keeps failing does
// not wake its parent every time.
func (s *Service) failService(ctx context.Context, sub store.Subscription, parent contract.SessionID, cause contract.FailureCause, message string) error {
	now := s.now()
	outcome := store.HookOutcome{Failures: sub.Failures + 1, Result: contract.HookFailed}
	outcome.HoldUntil = now.Add(serviceBackoff(outcome.Failures))
	finished, err := s.event(sub.Room, engineSender, contract.HookFinished{Subscription: sub.ID, Result: contract.HookFailed, Cause: cause, Output: tail(message), Failures: outcome.Failures, RetryAt: &outcome.HoldUntil})
	if err != nil {
		return err
	}
	events := []contract.Event{finished}
	if outcome.Failures == 1 {
		notice, err := s.event(sub.Room, engineSender, contract.NoticePosted{Text: fmt.Sprintf("Workflow service %s (%s) failed: %s. It starts again at %s, and waits longer after each failure in a row. Events wait in its queue. To fix the script: subscription update %s --workflow FILE, which also ends the wait. subscription list shows later failures.", serviceName(sub), sub.ID, harness.Mask(tail(message)), outcome.HoldUntil.Format("15:04 MST"), sub.ID)})
		if err != nil {
			return err
		}
		events = append(events, notice)
	}
	return s.store.FailService(ctx, sub.ID, outcome, parent, events...)
}

// noticeParent tells the parent of a service's run something that happened to one event.
func (s *Service) noticeParent(ctx context.Context, run store.WorkflowRun, text string) error {
	parent, err := s.runParent(ctx, run)
	if err != nil {
		return err
	}
	notice, err := s.event(run.Room, engineSender, contract.NoticePosted{Text: text})
	if err != nil {
		return err
	}
	return s.store.NoticeSession(ctx, parent, notice)
}

// runParent is the session a run reports to: its parent in the room's tree.
func (s *Service) runParent(ctx context.Context, run store.WorkflowRun) (contract.SessionID, error) {
	tree, err := s.store.RoomTree(ctx, run.Room)
	return tree.Parent(run.Session), err
}

// serviceCaller finds the service run the calling box hosts.
func (s *Service) serviceCaller(ctx context.Context, caller contract.Principal) (store.WorkflowRun, *contract.Error) {
	run, problem := s.callerWorkflow(ctx, caller)
	if problem != nil {
		return run, problem
	}
	if run.Subscription == "" || run.Key != "" || run.State != contract.WorkflowRunning {
		return run, invalid("events belong to a running workflow service")
	}
	return run, nil
}

// WorkflowEvents hands a service's box the rows it should handle. A disabled or paused subscription hands out nothing.
func (s *Service) WorkflowEvents(ctx context.Context, caller contract.Principal, req contract.WorkflowEventsRequest) (contract.WorkflowEventsResponse, *contract.Error) {
	run, problem := s.serviceCaller(ctx, caller)
	if problem != nil {
		return contract.WorkflowEventsResponse{}, problem
	}
	if req.Boot == "" || len(req.Boot) > 64 {
		return contract.WorkflowEventsResponse{}, invalid("boot names this start of the box, at most 64 characters")
	}
	sub, err := s.store.SubscriptionByID(ctx, run.Subscription)
	if err != nil {
		return contract.WorkflowEventsResponse{}, failure(err)
	}
	if sub.Disabled || sub.Held {
		return contract.WorkflowEventsResponse{Events: []contract.WorkflowEvent{}}, nil
	}
	events, dropped, err := s.store.HandOverEvents(ctx, run, req.Boot, req.Held, min(max(req.Limit, 1), handOverBatch), s.now())
	if err != nil {
		return contract.WorkflowEventsResponse{}, failure(err)
	}
	for _, event := range dropped {
		if !event.FirstOfKey {
			continue
		}
		text := fmt.Sprintf("Workflow service %s dropped an event for key %q: its box took it %d times and never finished it. Later failures of this key stay quiet until one event of it succeeds.", sub.ID, event.Key, event.HandOvers)
		if err := s.noticeParent(ctx, run, text); err != nil {
			return contract.WorkflowEventsResponse{}, failure(err)
		}
	}
	if events == nil {
		events = []contract.WorkflowEvent{}
	}
	return contract.WorkflowEventsResponse{Events: events}, nil
}

// WorkflowEventDone acknowledges a row: its handler ended, and the row leaves the queue with the steps it made. A handler that
// threw also tells the parent, once for each streak of failures of its key.
func (s *Service) WorkflowEventDone(ctx context.Context, caller contract.Principal, req contract.WorkflowEventDoneRequest) (contract.WorkflowEventDoneResponse, *contract.Error) {
	run, problem := s.serviceCaller(ctx, caller)
	if problem != nil {
		return contract.WorkflowEventDoneResponse{}, problem
	}
	key, first, err := s.store.FinishEvent(ctx, run, req.Row, req.Error != "", s.now())
	if err != nil {
		return contract.WorkflowEventDoneResponse{}, failure(err)
	}
	if first {
		text := fmt.Sprintf("Workflow service %s: the handler for key %q threw: %s. It goes on with other events. Later failures of this key stay quiet until one event of it succeeds.", run.Subscription, key, harness.Mask(clipText(req.Error, 500)))
		if err := s.noticeParent(ctx, run, text); err != nil {
			return contract.WorkflowEventDoneResponse{}, failure(err)
		}
	}
	return contract.WorkflowEventDoneResponse{}, nil
}

func clipText(text string, n int) string {
	if runes := []rune(text); len(runes) > n {
		return string(runes[:n]) + "…"
	}
	return text
}

func (s *Service) WorkflowState(ctx context.Context, caller contract.Principal, req contract.WorkflowStateRequest) (contract.WorkflowStateResponse, *contract.Error) {
	run, problem := s.serviceCaller(ctx, caller)
	if problem != nil {
		return contract.WorkflowStateResponse{}, invalid("state belongs to an event handler of a workflow service")
	}
	if len(req.Value) > 65536 || len(req.Value) > 0 && !json.Valid(req.Value) {
		return contract.WorkflowStateResponse{}, invalid("state is JSON of at most 64 KiB")
	}
	value, err := s.store.SubscriptionWorkflowState(ctx, run.ID, req.Row, req.Value)
	if err != nil {
		return contract.WorkflowStateResponse{}, failure(err)
	}
	return contract.WorkflowStateResponse{Value: value}, nil
}

// serviceName is the workflow's name from its script's meta, which tells a person which service a notice is about.
func serviceName(sub store.Subscription) string {
	meta, _ := workflowmeta.Parse(sub.Workflow.Script)
	return meta.Name
}
