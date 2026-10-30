package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) Events(ctx context.Context, caller contract.Principal, room contract.RoomID, after int64) (<-chan contract.Event, *contract.Error) {
	if e := s.roomAccess(ctx, caller, room); e != nil {
		return nil, e
	}
	sub, cancel := s.store.Subscribe()
	out := make(chan contract.Event)
	go func() {
		defer close(out)
		defer func() { cancel() }()
		for {
			events, err := s.store.Events(ctx, room, after, 256, nil)
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

type boxChannel struct{ stop context.CancelFunc }

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
	if previous := s.channels[session.ID]; previous != nil {
		previous.stop()
	}
	channel := &boxChannel{stop: stop}
	s.channels[session.ID] = channel
	s.mu.Unlock()
	sub, cancel := s.store.Subscribe()
	out := make(chan contract.BoxMessage)
	go func() {
		defer func() {
			stop()
			s.mu.Lock()
			if s.channels[session.ID] == channel {
				delete(s.channels, session.ID)
			}
			s.mu.Unlock()
		}()
		defer close(out)
		defer func() { cancel() }()
		replay := true
		archiveSent := false
		for {
			current, err := s.store.Box(ctx, session.Box)
			if err != nil || current.State == contract.BoxDeleted {
				return
			}
			if current.State == contract.BoxClosing {
				if current.Reason != "" {
					archiveSent = false
				}
				if !archiveSent && current.Reason == "" {
					if session.Repo != "" {
						select {
						case out <- contract.BoxMessage{Body: contract.Archive{}}:
							archiveSent = true
						case <-ctx.Done():
							return
						}
					}
				}
				select {
				case event, ok := <-sub:
					if !ok {
						return
					}
					if body, ok := event.Body.(contract.BoxChanged); ok && body.Box == session.Box && body.To == contract.BoxClosing && body.Reason != "" {
						archiveSent = false
					}
				case <-ctx.Done():
					return
				}
				continue
			}
			s.mu.Lock()
			turnID, err := id("turn_", s.now())
			if err != nil {
				s.mu.Unlock()
				return
			}
			started, err := s.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
			if err != nil {
				s.mu.Unlock()
				return
			}
			turn, deliveries, err := s.store.Dispatch(ctx, session.ID, contract.TurnID(turnID), started, replay)
			s.mu.Unlock()
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
					text = fmt.Sprintf("Goal: %s\n\n%s", body.Goal, body.Brief)
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
			case _, ok := <-sub:
				if !ok {
					if ctx.Err() != nil {
						return
					}
					cancel()
					sub, cancel = s.store.Subscribe()
					replay = true
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
