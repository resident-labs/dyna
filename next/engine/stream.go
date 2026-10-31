package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
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
	live := make(chan contract.Event, 16)
	s.mu.Lock()
	s.live[live] = room
	s.mu.Unlock()
	out := make(chan contract.Event)
	go func() {
		defer close(out)
		defer func() { s.mu.Lock(); delete(s.live, live); s.mu.Unlock() }()
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
			case event := <-live:
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

type boxChannel struct {
	stop          context.CancelFunc
	wake          chan struct{}
	watching      uint64
	shellWatching uint64
	archiveRetry  bool
}

func (s *Service) BoxChannel(ctx context.Context, caller contract.Principal) (<-chan contract.BoxMessage, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return nil, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return nil, failure(err)
	}
	ctx, stop := context.WithCancel(ctx)
	s.mu.Lock()
	state := s.sessionLocked(session.ID)
	if previous := state.channel; previous != nil {
		s.clearComputerLocked(session.ID)
		s.clearShellLocked(session.ID)
		previous.stop()
	}
	channel := &boxChannel{stop: stop, wake: make(chan struct{}, 1)}
	state.channel = channel
	s.mu.Unlock()
	out := make(chan contract.BoxMessage)
	go func() {
		defer func() {
			stop()
			s.mu.Lock()
			if state.channel == channel {
				state.channel = nil
				s.clearComputerLocked(session.ID)
				s.clearShellLocked(session.ID)
			}
			s.mu.Unlock()
		}()
		defer close(out)
		replay := true
		archiveSent := false
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
				return
			}
			current, err := s.store.Box(ctx, session.Box)
			if err != nil || current.State == contract.BoxDeleted {
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
						return
					}
				}
				select {
				case <-channel.wake:
					continue
				case <-ctx.Done():
					return
				}
				continue
			}
			unlock := s.lockBox(session.ID)
			allowed, err := s.store.CanDispatch(ctx, session.ID)
			if err != nil {
				unlock()
				return
			}
			if !allowed {
				unlock()
				select {
				case <-channel.wake:
					continue
				case <-ctx.Done():
					return
				}
			}
			turnID, err := id("turn_", s.now())
			if err != nil {
				unlock()
				return
			}
			started, err := s.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
			if err != nil {
				unlock()
				return
			}
			turn, deliveries, err := s.dispatch(ctx, session.ID, contract.TurnID(turnID), started, replay)
			unlock()
			if err != nil {
				return
			}
			replay = false
			var start []contract.TurnInput
			var steer []contract.TurnSteer
			for _, delivery := range deliveries {
				event, err := s.store.Event(ctx, int64(delivery.Seq))
				if err != nil {
					return
				}
				var text string
				switch body := event.Body.(type) {
				case contract.PostCreated:
					text = body.Text
				case contract.SessionStarted:
					var parts []string
					if body.Goal != "" {
						parts = append(parts, "Goal: "+body.Goal)
					}
					if body.Brief != "" {
						parts = append(parts, body.Brief)
					}
					text = strings.Join(parts, "\n\n")
				case contract.OpRequested:
					card, err := json.Marshal(body.Card)
					if err != nil {
						return
					}
					text = fmt.Sprintf("Approval requested: %s\n%s\nDecide with dyna op approve %s or dyna op deny %s --reason \"...\"", body.ID, card, body.ID, body.ID)
				case contract.PrOpened:
					text = fmt.Sprintf("PR opened: %s", body.URL)
				case contract.OpDecided:
					text = fmt.Sprintf("Operation %s: %s. %s", body.ID, body.State, body.Decision.Reason)
				case contract.OpRan:
					text = fmt.Sprintf("Operation %s: %s. %s\n%s", body.ID, body.State, body.Result.Error, body.Result.Output)
				case contract.TurnEnded:
					text = body.Final
					if body.Outcome == contract.TurnFailed {
						text = "turn failed: " + body.Error
					}
				case contract.ShellJobEnded:
					text = shellJobText(body)
				default:
					return
				}
				input := contract.TurnInput{Delivery: delivery.Seq, Surface: delivery.Surface, Reply: delivery.Reply, Text: text}
				if delivery.Queue == nil {
					start = append(start, input)
				} else {
					steer = append(steer, contract.TurnSteer{Input: input, Queue: *delivery.Queue})
				}
			}
			if len(start) != 0 {
				select {
				case out <- contract.BoxMessage{Body: contract.TurnStart{Turn: turn, Inputs: start}}:
				case <-ctx.Done():
					return
				}
			}
			for _, input := range steer {
				select {
				case out <- contract.BoxMessage{Body: input}:
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-channel.wake:
				continue
			case <-ctx.Done():
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
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	for _, id := range ids {
		if _, ok := s.wakePending[id]; !ok {
			s.wakePending[id] = false
		}
	}
	for _, event := range events {
		switch body := event.Body.(type) {
		case contract.TurnEnded:
			if _, ok := s.wakePending[body.Session]; !ok {
				s.wakePending[body.Session] = false
			}
		case contract.BoxChanged:
			s.wakePending[body.Session] = s.wakePending[body.Session] || (body.To == contract.BoxClosing && body.Reason != "")
		}
	}
	if len(s.wakePending) != 0 {
		select {
		case s.wakeSignal <- struct{}{}:
		default:
		}
	}
}

func (s *Service) wakeSessions(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-s.wakeSignal:
		}
		s.wakeMu.Lock()
		pending := s.wakePending
		s.wakePending = make(map[contract.SessionID]bool)
		s.wakeMu.Unlock()
		s.mu.Lock()
		for id, retry := range pending {
			if state := s.sessions[id]; state != nil && state.channel != nil {
				state.channel.archiveRetry = state.channel.archiveRetry || retry
				wakeChannel(state.channel)
			}
		}
		s.mu.Unlock()
	}
}
