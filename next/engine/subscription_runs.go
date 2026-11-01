package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

const (
	dispatchBatch = 256
	// runGrace is how far past its limit a Run may go before the engine stops waiting for its host.
	runGrace = 5 * time.Minute
	// A failing Run waits one period, doubling up to the cap. An event subscription has no period, so it starts at a minute.
	backoffCap = 6 * time.Hour
	// hookOutputBytes is how much of a Run's output its event keeps.
	hookOutputBytes = 8 << 10
	hookWatchEvery  = 10 * time.Second
)

var engineSender = contract.Principal{Kind: contract.PrincipalEngine}

// HookRunner runs subscription Runs in boxes on hosts.
type HookRunner interface {
	StartRun(context.Context, box.RunSpec) error
	RestoreRun(context.Context, box.RunSpec) error
	RunStatus(context.Context, contract.HostName, contract.BoxID) (box.RunStatus, error)
	RunOutput(context.Context, contract.HostName, contract.BoxID) ([]byte, error)
	RemoveRun(context.Context, contract.HostName, contract.BoxID) error
}

// hookStarts holds the Runs whose start is still going on in this process, which a watch must leave alone.
// A Run left in the store with no entry here is one the last engine started, so a watch reads it from its host.
type hookStarts struct {
	mu     sync.Mutex
	active map[contract.BoxID]bool
	group  sync.WaitGroup
}

func (h *hookStarts) add(id contract.BoxID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == nil {
		h.active = make(map[contract.BoxID]bool)
	}
	h.active[id] = true
	h.group.Add(1)
}

func (h *hookStarts) done(id contract.BoxID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.active, id)
	h.group.Done()
}

func (h *hookStarts) has(id contract.BoxID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active[id]
}

// SetHookRunner configures Runs before the service starts. memory reads a host's latest memory use.
func (s *Service) SetHookRunner(r HookRunner, memory func(contract.HostName) int64) {
	s.hooks, s.hookMemory = r, memory
}

func (s *Service) pokeSubscriptions() {
	select {
	case s.hookPoke <- struct{}{}:
	default:
	}
}

// RunSubscriptions serves subscriptions until ctx ends. Dispatch and scheduling follow every commit. Runs are
// watched on a timer, because a host tells the engine nothing. A running box outlives the engine.
func (s *Service) RunSubscriptions(ctx context.Context) {
	if err := s.restoreRuns(ctx); err != nil && ctx.Err() == nil {
		log.Printf("subscriptions: restore runs: %v", err)
	}
	ticks := time.NewTicker(hookWatchEvery)
	defer ticks.Stop()
	defer s.hookStarts.group.Wait()
	for {
		watch := false
		select {
		case <-ctx.Done():
			return
		case <-s.hookPoke:
		case <-ticks.C:
			watch = true
		}
		if err := s.subscriptionPass(ctx, watch); err != nil && ctx.Err() == nil {
			log.Printf("subscriptions: %v", err)
		}
	}
}

func (s *Service) subscriptionPass(ctx context.Context, watch bool) error {
	var failures []error
	for {
		count, err := s.store.DispatchEvents(ctx, dispatchBatch)
		if err != nil {
			failures = append(failures, err)
			break
		}
		if count < dispatchBatch {
			break
		}
	}
	failures = append(failures, s.fireDue(ctx))
	if watch {
		failures = append(failures, s.watchRuns(ctx))
	}
	return errors.Join(failures...)
}

func (s *Service) restoreRuns(ctx context.Context) error {
	if s.hooks == nil {
		return nil
	}
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, run := range runs {
		failures = append(failures, s.hooks.RestoreRun(ctx, box.RunSpec{ID: run.ID, Subscription: run.Subscription, Host: run.Host, Room: run.Room}))
	}
	return errors.Join(failures...)
}

// fireDue delivers scheduled wakes and starts Runs. A Run with no room for it now stays due.
func (s *Service) fireDue(ctx context.Context) error {
	now := s.now()
	due, err := s.store.DueSubscriptions(ctx, now)
	if err != nil {
		return err
	}
	var failures []error
	for _, sub := range due {
		if sub.Wake != nil {
			event, err := s.event(sub.Room, engineSender, contract.HookDue{Subscription: sub.ID})
			if err == nil {
				err = s.store.FireScheduledWake(ctx, sub, event)
			}
			failures = append(failures, err)
			continue
		}
		if s.hooks != nil {
			failures = append(failures, s.beginRun(ctx, sub, now))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) beginRun(ctx context.Context, sub store.Subscription, now time.Time) error {
	size, ok := box.Size(sub.Run.Size)
	if !ok {
		return fmt.Errorf("subscription %s: invalid size %q", sub.ID, sub.Run.Size)
	}
	value, err := id("run_", now)
	if err != nil {
		return err
	}
	run := store.HookRun{ID: contract.BoxID(value), Subscription: sub.ID, Room: sub.Room, Size: size.Name, StartedAt: now}
	if run.Host, err = s.placeRun(ctx, sub, run.ID, size); err != nil || run.Host == "" {
		return err
	}
	begun, err := s.store.BeginRun(ctx, sub, run)
	if err != nil || !begun {
		return err
	}
	s.hookStarts.add(run.ID)
	go func() {
		defer s.hookStarts.done(run.ID)
		err := s.hooks.StartRun(ctx, box.RunSpec{ID: run.ID, Subscription: sub.ID, Host: run.Host, Room: run.Room, Image: sub.Run.Image, Size: size, Command: sub.Run.Command, Files: sub.Run.Files})
		if err == nil {
			return
		}
		// The engine stopping is no fault of the Run. The next engine reads the row, finds no container and says so.
		if ctx.Err() != nil {
			return
		}
		log.Printf("subscription %s: start run: %v", sub.ID, err)
		if err := s.finishRun(context.WithoutCancel(ctx), run, sub, runResult{cause: contract.CauseStartFailed, output: err.Error()}); err != nil {
			log.Printf("subscription %s: finish run: %v", sub.ID, err)
		}
	}()
	return nil
}

// placeRun picks a host the way admission picks one for a box. An empty host means wait.
func (s *Service) placeRun(ctx context.Context, sub store.Subscription, id contract.BoxID, size contract.BoxSize) (contract.HostName, error) {
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return "", err
	}
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		return "", err
	}
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		return "", err
	}
	for _, run := range runs {
		active, _ := box.Size(run.Size)
		boxes = append(boxes, box.Box{ID: run.ID, Room: run.Room, Host: run.Host, Image: "run", Size: active, State: contract.BoxReady})
	}
	if sub.Room != "" {
		limits, err := s.store.RoomLimits(ctx, sub.Room)
		if err != nil {
			return "", err
		}
		cpus, memory := size.CPUs, size.MemBytes
		for _, b := range boxes {
			if b.Room == sub.Room && b.State != contract.BoxPending && b.State != contract.BoxStopped && b.State != contract.BoxFailed && b.State != contract.BoxDeleted {
				cpus += b.Size.CPUs
				memory += b.Size.MemBytes
			}
		}
		if limits.CPUs > 0 && cpus > 4*limits.CPUs || limits.MemBytes > 0 && memory > 4*limits.MemBytes {
			return "", nil
		}
	}
	for i := range hosts {
		if s.hookMemory != nil {
			hosts[i].MemUsed = s.hookMemory(hosts[i].Name)
		}
	}
	return box.Admit(hosts, boxes, box.Box{ID: id, Room: sub.Room, Size: size}).Host, nil
}

type runResult struct {
	stopped  bool
	exitCode int
	cause    contract.FailureCause
	output   string
}

// watchRuns ends each Run whose box has stopped, whose time is up, or whose reason to run has gone.
func (s *Service) watchRuns(ctx context.Context) error {
	if s.hooks == nil {
		return nil
	}
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, run := range runs {
		if !s.hookStarts.has(run.ID) {
			failures = append(failures, s.watchRun(ctx, run))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) watchRun(ctx context.Context, run store.HookRun) error {
	sub, err := s.store.SubscriptionByID(ctx, run.Subscription)
	removed := errors.Is(err, store.ErrNotFound)
	if err != nil && !removed {
		return err
	}
	if !removed && run.Room != "" {
		if err := s.store.RoomOpen(ctx, run.Room); errors.Is(err, store.ErrRoomDeleting) {
			removed = true
		} else if err != nil {
			return err
		}
	}
	status, err := s.hooks.RunStatus(ctx, run.Host, run.ID)
	if err != nil {
		if s.now().Before(run.StartedAt.Add(box.RunLimit + runGrace)) {
			return nil
		}
		// Whatever the host still holds goes with its next maintenance. The socket here goes now.
		cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := s.hooks.RemoveRun(cleanup, run.Host, run.ID); err != nil {
			log.Printf("subscription %s: remove run: %v", run.Subscription, err)
		}
		return s.finishRun(ctx, run, sub, runResult{cause: contract.CauseHostLost, output: err.Error()})
	}
	switch {
	case removed:
		if err := s.hooks.RemoveRun(ctx, run.Host, run.ID); err != nil {
			return err
		}
		return s.finishRun(ctx, run, sub, runResult{stopped: true})
	case !status.Found:
		if err := s.hooks.RemoveRun(ctx, run.Host, run.ID); err != nil {
			return err
		}
		return s.finishRun(ctx, run, sub, runResult{cause: contract.CauseInterrupted})
	case status.Running && s.now().Before(run.StartedAt.Add(box.RunLimit+runGrace)):
		return nil
	}
	out, err := s.hooks.RunOutput(ctx, run.Host, run.ID)
	if err != nil {
		return err
	}
	if err := s.hooks.RemoveRun(ctx, run.Host, run.ID); err != nil {
		return err
	}
	result := runResult{exitCode: status.ExitCode, output: string(out)}
	if status.Running || !s.now().Before(run.StartedAt.Add(box.RunLimit)) && status.ExitCode != 0 {
		result.cause = contract.CauseRunTimeout
	}
	return s.finishRun(ctx, run, sub, result)
}

// tail keeps the end of a Run's output, on a character boundary.
func tail(output string) string {
	if len(output) <= hookOutputBytes {
		return output
	}
	return strings.ToValidUTF8(output[len(output)-hookOutputBytes:], "")
}

// backoff doubles from one period with each failure in a row, up to a cap.
func backoff(every time.Duration, failures int) time.Duration {
	wait := max(every, time.Minute)
	for range failures - 1 {
		if wait >= backoffCap {
			break
		}
		wait *= 2
	}
	return min(wait, backoffCap)
}

// finishRun records one Run's result. The first failure in a row also posts a notice, and the rest stay quiet
// in hook.finished: a watcher that keeps failing must not wake root every time.
func (s *Service) finishRun(ctx context.Context, run store.HookRun, sub store.Subscription, result runResult) error {
	now := s.now()
	body := contract.HookFinished{Subscription: run.Subscription, ExitCode: result.exitCode, Cause: result.cause, Output: tail(result.output)}
	outcome := store.HookOutcome{Failures: sub.Failures}
	switch {
	case result.stopped:
		body.Result = contract.HookStopped
	case result.exitCode == 0 && result.cause == "":
		body.Result = contract.HookPassed
		outcome.Failures = 0
	default:
		body.Result = contract.HookFailed
		outcome.Failures = sub.Failures + 1
		outcome.HoldUntil = now.Add(backoff(time.Duration(sub.Every)*time.Millisecond, outcome.Failures))
		body.Failures = outcome.Failures
		body.RetryAt = &outcome.HoldUntil
	}
	outcome.Result = body.Result
	event, err := s.event(run.Room, engineSender, body)
	if err != nil {
		return err
	}
	events := []contract.Event{event}
	if body.Result == contract.HookFailed && outcome.Failures == 1 {
		notice, err := s.event(run.Room, engineSender, contract.NoticePosted{Text: fmt.Sprintf("Subscription %s failed and will retry after a wait. It reports again only once it has recovered and failed anew. Its last result is in hook.finished in the room log.", run.Subscription)})
		if err != nil {
			return err
		}
		events = append(events, notice)
	}
	return s.store.FinishRun(ctx, run, outcome, events...)
}

// HookPost posts in a Run's room. It takes text and mentions, and with no mention it goes to root. Apart from
// HookTrack, a Run has no other way to act on the room, so a script cannot use it to reach anything a post cannot.
func (s *Service) HookPost(ctx context.Context, sub contract.SubscriptionID, id contract.RoomID, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
	if id == "" {
		return contract.PostResponse{}, invalid("a machine run has no room to post in")
	}
	if req.Room != "" && req.Room != id {
		return contract.PostResponse{}, forbidden()
	}
	if strings.TrimSpace(req.Text) == "" {
		return contract.PostResponse{}, invalid("post text is required")
	}
	if len(req.Attachments) > 0 || req.Home != (contract.Conversation{}) || req.ReplyTo != "" || req.ClientID != "" || req.ToolCallID != "" || req.VisualUpdate != "" || req.VisualReply != "" {
		return contract.PostResponse{}, invalid("a run posts text and mentions only")
	}
	tree, err := s.store.RoomTree(ctx, id)
	if err != nil {
		return contract.PostResponse{}, failure(err)
	}
	var mentions []contract.Principal
	var recipients []room.Recipient
	for _, name := range req.Mentions {
		session, err := s.store.SessionByName(ctx, id, strings.TrimPrefix(name, "@"))
		if errors.Is(err, store.ErrNotFound) {
			return contract.PostResponse{}, invalid("unknown session @" + strings.TrimPrefix(name, "@"))
		}
		if err != nil {
			return contract.PostResponse{}, failure(err)
		}
		mentions = append(mentions, contract.Principal{Kind: contract.PrincipalSession, Session: session.ID})
		recipients = append(recipients, room.Recipient{Session: session.ID, Explicit: true})
	}
	if len(recipients) == 0 {
		recipients = []room.Recipient{{Session: tree.Root(), Explicit: true}}
	}
	home := contract.Conversation{Kind: contract.ConversationFeed}
	event, err := s.event(id, engineSender, contract.PostCreated{Text: req.Text, Mentions: mentions, Home: home, Hook: sub})
	if err != nil {
		return contract.PostResponse{}, failure(err)
	}
	deliveries := tree.Deliveries(room.DeliveryInput{Event: event, Surface: contract.SurfaceService, Recipients: recipients, HasMentions: len(mentions) > 0, Home: home})
	posted, seq, err := s.store.PostOnce(ctx, event, deliveries, "", "")
	if err != nil {
		return contract.PostResponse{}, failure(err)
	}
	return contract.PostResponse{Event: posted, Seq: seq}, nil
}
