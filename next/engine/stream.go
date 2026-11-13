package engine

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"time"

	"github.com/resident-labs/dyna/engine/prompt"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/live"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

func (s *Service) Events(ctx context.Context, caller contract.Principal, room contract.RoomID, after int64) (<-chan contract.Event, *contract.Error) {
	return s.events(ctx, caller, room, after, nil)
}

// CombinedEvents carries owner attention and the selected room on one ordered stream.
func (s *Service) CombinedEvents(ctx context.Context, caller contract.Principal, room contract.RoomID, after, roomAfter int64) (<-chan contract.Event, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return nil, forbidden()
	}
	if room == "" || after < 0 || roomAfter < 0 {
		return nil, invalid("invalid combined stream")
	}
	return s.events(ctx, caller, room, after, &roomAfter)
}

func (s *Service) events(ctx context.Context, caller contract.Principal, room contract.RoomID, after int64, roomAfter *int64) (<-chan contract.Event, *contract.Error) {
	if room == "" && caller.Kind != contract.PrincipalOwner {
		return nil, forbidden()
	}
	if room != "" {
		if e := s.roomAccess(ctx, caller, room); e != nil {
			return nil, e
		}
	}
	sub, cancel := s.store.Subscribe()
	liveRoom := room
	attentionAfter := after
	if roomAfter != nil {
		liveRoom = ""
		after = min(after, *roomAfter)
	}
	subscription, liveEvents := s.live.Subscribe(liveRoom, 16)
	out := make(chan contract.Event)
	go func() {
		defer close(out)
		defer s.live.Unsubscribe(subscription)
		defer func() { cancel() }()
		replay := true
		for {
			if replay {
				var events []contract.Event
				var err error
				if roomAfter != nil {
					events, err = s.store.CombinedEvents(ctx, room, max(after, attentionAfter), max(after, *roomAfter), 256)
				} else if room == "" {
					events, err = s.store.OwnerEvents(ctx, after, 256)
				} else {
					events, err = s.store.Events(ctx, room, after, 256, nil)
				}
				if err != nil {
					return
				}
				for _, event := range events {
					select {
					case out <- event:
						after = event.Seq
					case <-ctx.Done():
						return
					}
				}
				if len(events) == 256 {
					continue
				}
				replay = false
			}
			select {
			case event, ok := <-liveEvents:
				if !ok {
					return
				}
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			case event, ok := <-sub:
				if !ok {
					if ctx.Err() != nil {
						return
					}
					cancel()
					sub, cancel = s.store.Subscribe()
					replay = true
					continue
				}
				matches := store.StreamMatches(event, room)
				if roomAfter != nil {
					matches = event.Seq > attentionAfter && store.StreamMatches(event, "") || event.Seq > *roomAfter && event.Room == room
				}
				if event.Seq <= after || !matches {
					continue
				}
				select {
				case out <- event:
					after = event.Seq
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// streamStopReason describes why one box dispatch loop ended.
type streamStopReason string

const (
	streamCanceled             streamStopReason = "canceled"
	streamWatchStopped         streamStopReason = "watch_stopped"
	streamBoxReadFailed        streamStopReason = "box_read_failed"
	streamBoxDeleted           streamStopReason = "box_deleted"
	streamAdmissionFailed      streamStopReason = "admission_failed"
	streamIDFailed             streamStopReason = "id_failed"
	streamEventFailed          streamStopReason = "event_failed"
	streamDispatchFailed       streamStopReason = "dispatch_failed"
	streamDeliveryReadFailed   streamStopReason = "delivery_read_failed"
	streamDeliveryRenderFailed streamStopReason = "delivery_render_failed"
	streamFrameFailed          streamStopReason = "frame_failed"
	streamProfileFailed        streamStopReason = "profile_failed"
	streamSectionsReadFailed   streamStopReason = "sections_read_failed"
	streamSnapshotFailed       streamStopReason = "snapshot_failed"
	streamSectionsSaveFailed   streamStopReason = "sections_save_failed"
	streamModelSaveFailed      streamStopReason = "model_save_failed"
	streamStopReadFailed       streamStopReason = "stop_read_failed"
	streamWorkflowReadFailed   streamStopReason = "workflow_read_failed"
)

var quotedDynaMarker = regexp.MustCompile(`(?im)^([ \t]*)\[[ \t]*dyna[ \t]*\]`)

// applyUpdate runs for a stale box each time its box loop wakes. Between turns it stops the box, and when something on the list
// holds the box up past its turn it says what, once per box per update: the binary it waits for, or the mark that asked for it.
func (s *Service) applyUpdate(ctx context.Context, session store.Session, update string) error {
	stopping, err := s.store.StopStale(ctx, []contract.SessionID{session.ID})
	if err != nil {
		return err
	}
	if len(stopping) > 0 {
		s.reconciler.Kick()
		return nil
	}
	held, err := s.store.Responsibilities(ctx, session.ID)
	if err != nil {
		return err
	}
	text := room.BoxUpdateNotice(held)
	if text == "" {
		return nil
	}
	notice, err := s.engineEvent(session.Room, contract.SessionNotice{Session: session.ID, Text: text, ClientID: store.BoxUpdateNoticeID(session.Box, update)})
	if err != nil {
		return err
	}
	return s.store.SendSessionNotice(ctx, notice, session.ID)
}

// BoxChannel connects a box. binary is the checksum of the running box agent, empty from one too old to report it.
func (s *Service) BoxChannel(ctx context.Context, caller contract.Principal, binary string) (<-chan contract.BoxMessage, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return nil, forbidden()
	}
	session, open, err := s.store.OpenSession(ctx, caller.Session)
	if err != nil {
		return nil, failure(err)
	}
	if !open {
		return nil, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return nil, failure(err)
	}
	// The channel opens only after Pi finishes loading the saved conversation.
	if current.State == contract.BoxStarting {
		if _, err := s.transition(ctx, session.Room, current, contract.BoxReady, "", ""); err != nil {
			return nil, failure(err)
		}
	}
	if err := s.store.WakeLostInventory(ctx, session.Box); err != nil {
		return nil, failure(err)
	}
	_, err = s.store.SessionWorkflow(ctx, session.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, failure(err)
	}
	crew := err == nil
	ctx, stop := context.WithCancel(ctx)
	handle, problem := s.lockSession(ctx, session.ID)
	if problem != nil {
		stop()
		return nil, problem
	}
	previous := handle.Channel()
	var roomID contract.RoomID
	var stopped bool
	if previous != nil {
		roomID, stopped = handle.ClearComputer()
		handle.ClearShell()
	}
	channel := live.NewChannel(stop)
	handle.SetChannel(channel)
	stale := binary != "" && s.binary != "" && binary != s.binary
	handle.SetStale(stale)
	previous.Close()
	if stopped {
		_ = s.computerEvent(context.Background(), session.ID, roomID, contract.ComputerStopped)
	}
	handle.Unlock()
	slog.Info("box channel connected", "session", session.ID, "box", session.Box, "stale", stale)
	s.reconciler.Connected(session.Box)
	out := make(chan contract.BoxMessage)
	go func() {
		defer func() {
			if !handle.Lock() {
				stop()
				return
			}
			var roomID contract.RoomID
			var stopped bool
			if handle.Channel() == channel {
				handle.SetChannel(nil)
				roomID, stopped = handle.ClearComputer()
				handle.ClearShell()
			}
			channel.Close()
			if stopped {
				_ = s.computerEvent(context.Background(), session.ID, roomID, contract.ComputerStopped)
			}
			handle.Unlock()
		}()
		pumpDone := make(chan struct{})
		go func() {
			defer close(pumpDone)
			for {
				select {
				case ask := <-channel.Asks():
					select {
					case out <- contract.BoxMessage{Body: contract.BoxStatsAsk{Ask: ask}}:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
		defer func() {
			stop()
			<-pumpDone
			close(out)
		}()
		var batch <-chan time.Time
		var batchAt time.Time
		var stopBatch func()
		defer func() {
			if stopBatch != nil {
				stopBatch()
			}
		}()
		var reason streamStopReason
		var cause error
		defer func() { slog.Info("box dispatch stopped", "session", session.ID, "reason", reason, "err", cause) }()
		replay := true
		diffAsked := false
		var stopSent contract.TurnID
		pauseSent, stepsSent := false, []string(nil)
		var releasedSent []string
		reloadSent := 0 // Send current script and pause state on every channel; Reload ignores a script version it already has.
		for {
			if !handle.Lock() {
				return
			}
			compactTurn, compact := channel.TakeCompact()
			shellDesired := handle.ShellWatchGeneration()
			shellChanged := channel.ShellWatchChanged(shellDesired)
			handle.Unlock()
			if !sendWatch(ctx, out, shellChanged, shellDesired, contract.ShellWatchStart{Generation: shellDesired}, contract.ShellWatchStop{}) {
				reason, cause = streamWatchStopped, ctx.Err()
				return
			}
			if compact {
				select {
				case out <- contract.BoxMessage{Body: contract.Compact{Turn: compactTurn, OverTokens: idleCompactTokens}}:
				case <-ctx.Done():
					reason, cause = streamCanceled, ctx.Err()
					return
				}
			}
			current, err := s.store.Box(ctx, session.Box)
			if err != nil {
				reason, cause = streamBoxReadFailed, err
				return
			}
			if current.State == contract.BoxDeleted {
				reason = streamBoxDeleted
				return
			}
			if current.State == contract.BoxClosing {
				if !diffAsked {
					select {
					case out <- contract.BoxMessage{Body: contract.SaveDiff{Name: session.Name}}:
						diffAsked = true
					case <-ctx.Done():
						reason, cause = streamCanceled, ctx.Err()
						return
					}
				}
				select {
				case <-channel.WakeSignal():
					continue
				case <-ctx.Done():
					reason, cause = streamCanceled, ctx.Err()
					return
				}
			}
			// This comes before dispatch, or a waiting delivery would start the next turn each time and a busy box would never update.
			update := s.binary
			if !stale {
				update = handle.Refresh()
			}
			dispatch := s.store.Dispatch
			if update != "" {
				if err := s.applyUpdate(ctx, session, update); err != nil {
					reason, cause = streamAdmissionFailed, err
					return
				}
				dispatch = s.store.DispatchStale
			}
			// A reload reaches a crew's box on its next channel too, which ignores a version it already runs.
			if crew {
				run, err := s.store.SessionWorkflow(ctx, session.ID)
				if err != nil {
					reason, cause = streamWorkflowReadFailed, err
					return
				}
				if run.State == contract.WorkflowRunning && (run.Version != reloadSent || run.Paused != pauseSent || !slices.Equal(run.PausedSteps, stepsSent) || !slices.Equal(run.ReleasedSteps, releasedSent)) {
					workflow, err := s.boxWorkflow(ctx, run, current.Size.MemBytes)
					if err != nil {
						reason, cause = streamWorkflowReadFailed, err
						return
					}
					select {
					case out <- contract.BoxMessage{Body: contract.WorkflowReload{Workflow: *workflow}}:
						reloadSent = run.Version
						pauseSent, stepsSent = run.Paused, run.PausedSteps
						releasedSent = run.ReleasedSteps
					case <-ctx.Done():
						reason, cause = streamCanceled, ctx.Err()
						return
					}
				}
			}
			allowed, err := s.store.CanDispatch(ctx, session.ID)
			if err != nil {
				reason, cause = streamAdmissionFailed, err
				return
			}
			if !allowed {
				select {
				case <-channel.WakeSignal():
					continue
				case <-ctx.Done():
					reason, cause = streamCanceled, ctx.Err()
					return
				}
			}
			turnID, err := id("turn_", s.now())
			if err != nil {
				reason, cause = streamIDFailed, err
				return
			}
			started, err := s.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
			if err != nil {
				reason, cause = streamEventFailed, err
				return
			}
			turn, deliveries, profile, err := dispatch(ctx, session.ID, contract.TurnID(turnID), started, replay)
			if err != nil {
				reason, cause = streamDispatchFailed, err
				return
			}
			replay = false
			var start []contract.TurnInput
			var steer []contract.TurnSteer
			for _, delivery := range deliveries {
				event, err := s.store.Event(ctx, int64(delivery.Seq))
				if err != nil {
					reason, cause = streamDeliveryReadFailed, err
					return
				}
				if body, ok := event.Body.(contract.TurnEnded); ok && body.WaitingNotice != "" {
					// Keep the stored event whole; a waiting turn is not a result.
					body.Final = body.WaitingNotice
					body.Attachments, body.Data, body.DataAttachment = nil, "", nil
					event.Body = body
				}
				text, err := room.RenderDelivery(event.Body)
				if err != nil {
					reason, cause = streamDeliveryRenderFailed, err
					return
				}
				frame, files, err := s.inputFrame(ctx, session.ID, event, contract.Delivery{Surface: delivery.Surface, Reply: delivery.Reply, MeantFor: delivery.MeantFor, Why: delivery.Why})
				if err != nil {
					reason, cause = streamFrameFailed, err
					return
				}
				input := contract.TurnInput{Delivery: delivery.Seq, Surface: delivery.Surface, Reply: delivery.Reply, Text: frame + "\n" + quotedDynaMarker.ReplaceAllString(s.deliveryData(event, text), "${1}[quoted]"), Files: files}
				if post, ok := event.Body.(contract.PostCreated); ok {
					input.WorkflowStep = post.WorkflowStep
				}
				if delivery.Queue == nil {
					start = append(start, input)
				} else {
					var interrupt *contract.Interrupt
					if *delivery.Queue == contract.QueueSteer {
						interrupt, err = s.inputInterrupt(ctx, session.ID, event, delivery.Surface)
						if err != nil {
							reason, cause = streamFrameFailed, err
							return
						}
					}
					steer = append(steer, contract.TurnSteer{Input: input, Queue: *delivery.Queue, Interrupt: interrupt})
				}
			}
			if len(start) != 0 {
				model, _, err := s.profiles.Model(profile)
				if err != nil {
					reason, cause = streamProfileFailed, err
					return
				}
				if err = s.store.DispatchTurnModel(ctx, turn, model.ID); err != nil {
					reason, cause = streamModelSaveFailed, err
					return
				}
				sections, saved, err := s.store.SavedTurnSections(ctx, turn)
				if err != nil {
					reason, cause = streamSectionsReadFailed, err
					return
				}
				if !saved {
					snapshot, problem := s.ContextSnapshot(ctx, caller, contract.ContextSnapshotRequest{})
					if problem != nil {
						reason, cause = streamSnapshotFailed, errors.New(problem.Message)
						return
					}
					sections = snapshot.Sections
					sections, err = s.store.TurnSections(ctx, turn, sections)
					if err != nil {
						reason, cause = streamSectionsSaveFailed, err
						return
					}
				}
				select {
				case out <- contract.BoxMessage{Body: contract.TurnStart{Turn: turn, Inputs: start, Model: model, ModelInstructions: prompt.ModelInstructions(model), CompactTokens: s.profiles.CompactionTokens(profile), TextOnlyModels: s.profiles.TextOnlyModels(), Sections: sections}}:
				case <-ctx.Done():
					reason, cause = streamCanceled, ctx.Err()
					return
				}
			}
			stopTurn, err := s.store.StopRequested(ctx, session.ID)
			if err != nil {
				reason, cause = streamStopReadFailed, err
				return
			}
			if stopTurn != "" && stopTurn != stopSent {
				select {
				case out <- contract.BoxMessage{Body: contract.TurnStop{Turn: stopTurn, Reason: "Stopped by request"}}:
					stopSent = stopTurn
				case <-ctx.Done():
					reason, cause = streamCanceled, ctx.Err()
					return
				}
			}
			for _, input := range steer {
				select {
				case out <- contract.BoxMessage{Body: input}:
				case <-ctx.Done():
					reason, cause = streamCanceled, ctx.Err()
					return
				}
			}
			deadline, err := s.store.InputBatchDeadline(ctx, session.ID)
			if err != nil {
				reason, cause = streamDispatchFailed, err
				return
			}
			// One timer belongs to this connected stream, not each delivery.
			// Reconnect derives its deadline from the same committed turn end.
			if !deadline.IsZero() && !deadline.After(s.now()) {
				// Dispatch used started.Time. If the window ended while that
				// transaction ran, retry now rather than dropping the last kick.
				if started.Time.Before(deadline) {
					continue
				}
				deadline = time.Time{}
			}
			if !deadline.Equal(batchAt) {
				if stopBatch != nil {
					stopBatch()
					stopBatch = nil
				}
				batch, batchAt = nil, deadline
				if !deadline.IsZero() {
					batch, stopBatch = s.inputBatchTimer(deadline.Sub(s.now()))
				}
			}
			select {
			case <-batch:
				// A monotonic timer can expire before the persisted wall-clock
				// deadline after a clock correction. Forget its consumed identity
				// so a still-future deadline gets a fresh scheduled kick.
				batch, batchAt = nil, time.Time{}
			case <-channel.WakeSignal():
			case <-ctx.Done():
				reason, cause = streamCanceled, ctx.Err()
				return
			}
		}
	}()
	return out, nil
}

func sendWatch(ctx context.Context, out chan<- contract.BoxMessage, changed bool, desired uint64, start, stop contract.BoxMessageBody) bool {
	if !changed {
		return true
	}
	body := stop
	if desired != 0 {
		body = start
	}
	select {
	case out <- contract.BoxMessage{Body: body}:
		return true
	case <-ctx.Done():
		return false
	}
}

// queueWakes never waits for the engine mutex or the box loops.
func (s *Service) queueWakes(ids []contract.SessionID, events []contract.Event) {
	if len(ids) > 0 {
		s.reconciler.Kick()
	}
	for _, event := range events {
		switch event.Body.(type) {
		case contract.BoxChanged, contract.SessionStarted, contract.SessionClosed, contract.TurnEnded:
			s.reconciler.Kick()
		}
	}
	s.live.Wake(ids, events)
	if len(events) > 0 {
		s.pokeSubscriptions()
	}
}

func (s *Service) deliverWakes(pending map[contract.SessionID]struct{}) {
	for id := range pending {
		handle, ok := s.live.LookupSession(id)
		if !ok || !handle.Lock() {
			continue
		}
		if channel := handle.Channel(); channel != nil {
			channel.Wake()
		}
		handle.Unlock()
	}
}
