package engine

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const quietPeriod = 10 * time.Minute

type dormancy struct {
	mu          sync.Mutex
	unconfirmed map[contract.BoxID]bool
}

// ReconcileDormancy is level-triggered by committed events and the runtime tick.
func (s *Service) ReconcileDormancy(ctx context.Context, startup bool) {
	s.dormancy.mu.Lock()
	defer s.dormancy.mu.Unlock()
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		log.Printf("dormancy: %v", err)
		return
	}
	if startup {
		for _, b := range boxes {
			switch b.State {
			case contract.BoxStopped:
				s.dormancy.unconfirmed[b.ID] = true
			case contract.BoxStarting:
				if err := s.boxes.Start(ctx, b.ID); err != nil {
					log.Printf("dormancy start: %v", err)
				}
			}
		}
	}
	for id := range s.dormancy.unconfirmed {
		if err := s.boxes.Stop(ctx, id); err != nil {
			log.Printf("dormancy stop: %v", err)
		} else {
			delete(s.dormancy.unconfirmed, id)
		}
	}
	ids, err := s.store.DormancyCandidates(ctx, s.now().Add(-quietPeriod))
	if err != nil {
		log.Printf("dormancy: %v", err)
		return
	}
	for _, id := range ids {
		if err := s.reconcileBox(ctx, id); err != nil {
			log.Printf("dormancy: %v", err)
		}
	}
}

func (s *Service) reconcileBox(ctx context.Context, id contract.SessionID) error {
	unlock := s.lockBox(id)
	defer unlock()
	session, err := s.store.Session(ctx, id)
	if err != nil {
		return err
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return err
	}
	if s.dormancy.unconfirmed[current.ID] {
		return nil
	}
	if current.State == contract.BoxReady {
		changed, err := s.dormancyTransition(ctx, session.Room, current, contract.BoxStopped, "")
		if err != nil {
			return err
		}
		if changed {
			s.dormancy.unconfirmed[current.ID] = true
			if err := s.boxes.Stop(ctx, current.ID); err != nil {
				return err
			}
			delete(s.dormancy.unconfirmed, current.ID)
		}
		return nil
	}
	if current.State != contract.BoxStopped && current.State != contract.BoxPending {
		return nil
	}
	s.admit.Lock()
	defer s.admit.Unlock()
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		return err
	}
	placement, err := s.place(ctx, current, boxes)
	if err != nil {
		return err
	}
	if placement.Reason != "" {
		if current.State == contract.BoxStopped {
			_, err = s.dormancyTransition(ctx, session.Room, current, contract.BoxPending, string(placement.Reason))
		}
		return err
	}
	changed, err := s.dormancyTransition(ctx, session.Room, current, contract.BoxStarting, "")
	if err != nil || !changed {
		return err
	}
	if err := s.boxes.Start(ctx, current.ID); err != nil {
		current.State = contract.BoxStarting
		if _, rollback := s.dormancyTransition(ctx, session.Room, current, contract.BoxStopped, ""); rollback != nil {
			return rollback
		}
		return err
	}
	return nil
}

func (s *Service) dormancyTransition(ctx context.Context, room contract.RoomID, current box.Box, state contract.BoxState, reason string) (bool, error) {
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{})
	if err != nil {
		return false, err
	}
	if current.State == contract.BoxReady {
		return s.store.StopIdle(ctx, current, event, s.now().Add(-quietPeriod))
	}
	return s.store.Wake(ctx, current, state, reason, event)
}
