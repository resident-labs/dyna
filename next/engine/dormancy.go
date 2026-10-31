package engine

import (
	"context"
	"log"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const quietPeriod = 10 * time.Minute

type dormancy struct {
	unconfirmed  map[contract.BoxID]bool
	failedStarts map[contract.BoxID]bool
}

// ReconcileDormancy is level-triggered. The runtime serializes its calls.
func (s *Service) ReconcileDormancy(ctx context.Context, startup, retry bool) {
	s.drainMoveStops()
	if startup || retry {
		clear(s.dormancy.failedStarts)
		boxes, err := s.store.Boxes(ctx)
		if err != nil {
			log.Printf("dormancy: %v", err)
			return
		}
		for _, b := range boxes {
			if startup && b.State == contract.BoxStopped {
				s.dormancy.unconfirmed[b.ID] = true
			}
			if startup && b.State == contract.BoxStarting {
				unlock := s.lockBox(b.Session)
				current, err := s.store.Box(ctx, b.ID)
				if err == nil && current.State == contract.BoxStarting {
					err = s.startBox(ctx, current)
				}
				unlock()
				if err != nil {
					log.Printf("dormancy start: %v", err)
				}
			}
			if !retry || !s.dormancy.unconfirmed[b.ID] {
				continue
			}
			unlock := s.lockBox(b.Session)
			current, err := s.store.Box(ctx, b.ID)
			if err == nil && current.State == contract.BoxStopped {
				err = s.stopBox(ctx, b.ID)
			}
			unlock()
			if err != nil {
				log.Printf("dormancy stop: %v", err)
			}
		}
	}
	ids, err := s.store.DormancyCandidates(ctx, s.now().Add(-quietPeriod))
	if err != nil {
		log.Printf("dormancy: %v", err)
		return
	}
	for _, id := range ids {
		if err := s.wakeAncestors(ctx, id); err != nil {
			log.Printf("dormancy ancestors: %v", err)
			continue
		}
		if err := s.reconcileBox(ctx, id, false); err != nil {
			log.Printf("dormancy: %v", err)
		}
	}
}

func (s *Service) reconcileBox(ctx context.Context, id contract.SessionID, force bool) error {
	unlock := s.lockBox(id)
	defer unlock()
	s.drainMoveStops()
	session, err := s.store.Session(ctx, id)
	if err != nil {
		return err
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return err
	}
	if s.dormancy.unconfirmed[current.ID] || s.dormancy.failedStarts[current.ID] {
		return nil
	}
	if current.State == contract.BoxReady && force {
		return nil
	}
	if current.State == contract.BoxReady {
		changed, err := s.dormancyTransition(ctx, session.Room, current, contract.BoxStopped, "")
		if err != nil {
			return err
		}
		if changed {
			s.dormancy.unconfirmed[current.ID] = true
			return s.stopBox(ctx, current.ID)
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
	var changed bool
	if force {
		event, e := s.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{})
		if e != nil {
			return e
		}
		changed, err = s.store.WakeAncestor(ctx, current, event)
	} else {
		changed, err = s.dormancyTransition(ctx, session.Room, current, contract.BoxStarting, "")
	}
	if err != nil || !changed {
		return err
	}
	current.State = contract.BoxStarting
	return s.startBox(ctx, current)
}

func (s *Service) stopBox(ctx context.Context, id contract.BoxID) error {
	if err := s.boxes.Stop(ctx, id); err != nil {
		return err
	}
	delete(s.dormancy.unconfirmed, id)
	return nil
}

func (s *Service) startBox(ctx context.Context, current box.Box) error {
	var startErr error
	path, err := s.store.BoxAncestry(ctx, current.ID)
	if err != nil {
		return err
	}
	var ancestry []contract.BoxID
	for _, b := range path {
		ancestry = append(ancestry, b.ID)
	}
	slice, err := box.SliceName(ancestry)
	if err != nil {
		return err
	}
	if current.Slice != slice {
		var repos []contract.BoxRepo
		if s.repos != nil {
			repos, err = s.repos.Get(ctx, current.Session, current.Host, false)
			if err != nil {
				return err
			}
		}
		// Keep the old slice until recreation or close removes it.
		startErr = s.boxes.Recreate(ctx, BoxSpec{Box: current.ID, Session: current.Session, Host: current.Host, Slice: slice, Size: current.Size, Repos: repos}, current.Image, current.Slice)
		if startErr == nil {
			startErr = s.store.SetRecreated(ctx, current.ID, slice)
		}
	} else {
		startErr = s.boxes.Start(ctx, current.ID)
	}
	if err := startErr; err != nil {
		s.dormancy.failedStarts[current.ID] = true
		session, e := s.store.Session(ctx, current.Session)
		if e != nil {
			return e
		}
		if _, e = s.dormancyTransition(ctx, session.Room, current, contract.BoxStopped, ""); e != nil {
			return e
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

// wakeAncestors starts stopped budget parents before a child is admitted.
func (s *Service) wakeAncestors(ctx context.Context, id contract.SessionID) error {
	session, err := s.store.Session(ctx, id)
	if err != nil {
		return err
	}
	path, err := s.store.BoxAncestry(ctx, session.Box)
	if err != nil || len(path) == 0 {
		return err
	}
	current := path[len(path)-1]
	if current.State != contract.BoxStopped && current.State != contract.BoxPending {
		return nil
	}
	for _, parent := range path[:len(path)-1] {
		if err := s.reconcileBox(ctx, parent.Session, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) drainMoveStops() {
	s.moveMu.Lock()
	for _, id := range s.moveStops {
		s.dormancy.unconfirmed[id] = true
	}
	s.moveStops = nil
	s.moveMu.Unlock()
}
