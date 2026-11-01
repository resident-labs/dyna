package engine

import (
	"context"
	"errors"
	"log/slog"
	"regexp"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

func (s *Service) Events(ctx context.Context, caller contract.Principal, room contract.RoomID, after int64) (<-chan contract.Event, *contract.Error) {
	if room == "" && caller.Kind != contract.PrincipalOwner {
		return nil, forbidden()
	}
	if room != "" {
		if e := s.roomAccess(ctx, caller, room); e != nil {
			return nil, e
		}
	}
	sub, cancel := s.store.Subscribe()
	subscription, liveEvents := s.live.Subscribe(room, 16)
	out := make(chan contract.Event)
	go func() {
		defer close(out)
		defer s.live.Unsubscribe(subscription)
		defer func() { cancel() }()
		for {
			var events []contract.Event
			var err error
			if room == "" {
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
			case _, ok := <-sub:
				if !ok {
					if ctx.Err() != nil {
						return
					}
					cancel()
					sub, cancel = s.store.Subscribe()
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
	streamStopReadFailed       streamStopReason = "stop_read_failed"
)

var quotedDynaMarker = regexp.MustCompile(`(?im)^([ \t]*)\[[ \t]*dyna[ \t]*\]`)

type boxChannel struct {
	stop          context.CancelFunc
	wake          chan struct{}
	watching      uint64
	shellWatching uint64
	archiveRetry  bool
	input         computerInputHandle // owner input stays separate from the turn loop
}

func (s *Service) BoxChannel(ctx context.Context, caller contract.Principal) (<-chan contract.BoxMessage, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return nil, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return nil, failure(err)
	}
	if err := s.store.WakeLostInventory(ctx, session.Box); err != nil {
		return nil, failure(err)
	}
	ctx, stop := context.WithCancel(ctx)
	s.mu.Lock()
	state := s.sessionLocked(session.ID)
	s.mu.Unlock()
	state.computerLock.Lock()
	s.mu.Lock()
	previous := state.channel
	var roomID contract.RoomID
	var stopped bool
	if previous != nil {
		roomID, stopped = s.clearComputerLocked(session.ID)
		s.clearShellLocked(session.ID)
	}
	channel := &boxChannel{stop: stop, wake: make(chan struct{}, 1), input: computerInputHandle{queue: make(chan contract.ComputerInputBatch, 64), done: ctx.Done()}}
	state.channel = channel
	s.mu.Unlock()
	if previous != nil {
		previous.stop()
	}
	if stopped {
		_ = s.computerEvent(context.Background(), session.ID, roomID, contract.ComputerStopped)
	}
	state.computerLock.Unlock()
	s.reconciler.Connected(session.Box)
	out := make(chan contract.BoxMessage)
	go func() {
		defer func() {
			state.computerLock.Lock()
			stop()
			s.mu.Lock()
			var roomID contract.RoomID
			var stopped bool
			if state.channel == channel {
				state.channel = nil
				roomID, stopped = s.clearComputerLocked(session.ID)
				s.clearShellLocked(session.ID)
			}
			s.mu.Unlock()
			if stopped {
				_ = s.computerEvent(context.Background(), session.ID, roomID, contract.ComputerStopped)
			}
			state.computerLock.Unlock()
		}()
		pumpDone := make(chan struct{})
		go func() {
			defer close(pumpDone)
			for {
				select {
				case batch := <-channel.input.queue:
					select {
					case out <- contract.BoxMessage{Body: batch}:
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
		var reason streamStopReason
		var cause error
		defer func() { slog.Info("box dispatch stopped", "session", session.ID, "reason", reason, "err", cause) }()
		replay := true
		archiveSent := false
		var stopSent contract.TurnID
		for {
			s.mu.Lock()
			if channel.archiveRetry {
				archiveSent = false
				channel.archiveRetry = false
			}
			c := state.computer
			var desired uint64
			if c != nil && len(c.watchers) != 0 {
				desired = c.generation
			}
			var shellDesired uint64
			if w := state.watch; w != nil && len(w.watchers) > 0 {
				shellDesired = w.generation
			}
			s.mu.Unlock()
			if !s.sendWatch(ctx, out, desired, &channel.watching, contract.ComputerWatchStart{Generation: desired}, contract.ComputerWatchStop{}) ||
				!s.sendWatch(ctx, out, shellDesired, &channel.shellWatching, contract.ShellWatchStart{Generation: shellDesired}, contract.ShellWatchStop{}) {
				reason, cause = streamWatchStopped, ctx.Err()
				return
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
				if current.Reason != "" {
					archiveSent = false
				}
				if !archiveSent && current.Reason == "" {
					select {
					case out <- contract.BoxMessage{Body: contract.Archive{}}:
						archiveSent = true
					case <-ctx.Done():
						reason, cause = streamCanceled, ctx.Err()
						return
					}
				}
				select {
				case <-channel.wake:
					continue
				case <-ctx.Done():
					reason, cause = streamCanceled, ctx.Err()
					return
				}
			}
			allowed, err := s.store.CanDispatch(ctx, session.ID)
			if err != nil {
				reason, cause = streamAdmissionFailed, err
				return
			}
			if !allowed {
				select {
				case <-channel.wake:
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
			turn, deliveries, profile, err := s.store.Dispatch(ctx, session.ID, contract.TurnID(turnID), started, replay)
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
				text, err := room.RenderDelivery(event.Body)
				if err != nil {
					reason, cause = streamDeliveryRenderFailed, err
					return
				}
				frame, err := s.inputFrame(ctx, session.ID, event, contract.Delivery{Surface: delivery.Surface, Reply: delivery.Reply, HandoffFrom: delivery.HandoffFrom})
				if err != nil {
					reason, cause = streamFrameFailed, err
					return
				}
				input := contract.TurnInput{Delivery: delivery.Seq, Surface: delivery.Surface, Reply: delivery.Reply, Text: frame + "\n" + quotedDynaMarker.ReplaceAllString(text, "${1}[quoted]")}
				if delivery.Queue == nil {
					start = append(start, input)
				} else {
					steer = append(steer, contract.TurnSteer{Input: input, Queue: *delivery.Queue})
				}
			}
			if len(start) != 0 {
				model, _, err := s.profiles.Model(profile)
				if err != nil {
					reason, cause = streamProfileFailed, err
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
				case out <- contract.BoxMessage{Body: contract.TurnStart{Turn: turn, Inputs: start, Model: model, Sections: sections}}:
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
			select {
			case <-channel.wake:
				continue
			case <-ctx.Done():
				reason, cause = streamCanceled, ctx.Err()
				return
			}
		}
	}()
	return out, nil
}

func (s *Service) sendWatch(ctx context.Context, out chan<- contract.BoxMessage, desired uint64, sent *uint64, start, stop contract.BoxMessageBody) bool {
	s.mu.Lock()
	changed := desired != *sent
	*sent = desired
	s.mu.Unlock()
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
}

func (s *Service) deliverWakes(pending map[contract.SessionID]bool) {
	var channels []*boxChannel
	s.mu.Lock()
	for id, retry := range pending {
		if state := s.sessions[id]; state != nil && state.channel != nil {
			state.channel.archiveRetry = state.channel.archiveRetry || retry
			channels = append(channels, state.channel)
		}
	}
	s.mu.Unlock()
	for _, channel := range channels {
		wakeChannel(channel)
	}
}
