package engine

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// goalTextLimit caps every piece of goal text: a goal, its done condition, an item, a summary or a journal line.
const goalTextLimit = 16000

func checkGoalText(values ...string) *contract.Error {
	for _, value := range values {
		if utf8.RuneCountInString(value) > goalTextLimit {
			return invalid(fmt.Sprintf("goal text is longer than %d characters", goalTextLimit))
		}
	}
	return nil
}

// goalRoom finds the room a goal request acts in: the caller's own unless the request names it, and only one the caller can reach.
func (s *Service) goalRoom(ctx context.Context, caller contract.Principal, room contract.RoomID) (contract.RoomID, *contract.Error) {
	if room == "" {
		if caller.Kind != contract.PrincipalSession {
			return "", invalid("room is required")
		}
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return "", failure(err)
		}
		return session.Room, nil
	}
	if e := s.roomAccess(ctx, caller, room); e != nil {
		return "", e
	}
	if err := s.store.RoomExists(ctx, room); err != nil {
		return "", failure(err)
	}
	return room, nil
}

// goalProblem turns a refused goal write into its typed error. The tree checks run inside the write.
func goalProblem(err error) *contract.Error {
	var refusal *store.GoalRefusal
	switch {
	case errors.As(err, &refusal):
		return invalid(refusal.Message)
	case errors.Is(err, store.ErrTreeForbidden):
		return forbidden()
	}
	return failure(err)
}

func (s *Service) GoalSet(ctx context.Context, caller contract.Principal, req contract.GoalSetRequest) (contract.GoalSetResponse, *contract.Error) {
	if len(req.ClientID) > 64 {
		return contract.GoalSetResponse{}, invalid("clientId is longer than 64 characters")
	}
	if e := checkGoalText(req.Text, req.DoneWhen); e != nil {
		return contract.GoalSetResponse{}, e
	}
	room, e := s.goalRoom(ctx, caller, req.Room)
	if e != nil {
		return contract.GoalSetResponse{}, e
	}
	event, err := s.event(room, caller, contract.GoalSet{})
	if err != nil {
		return contract.GoalSetResponse{}, failure(err)
	}
	id, err := s.store.SetGoal(ctx, req, event)
	if err != nil {
		return contract.GoalSetResponse{}, goalProblem(err)
	}
	return contract.GoalSetResponse{Goal: id}, nil
}

func (s *Service) GoalList(ctx context.Context, caller contract.Principal, req contract.GoalListRequest) (contract.GoalListResponse, *contract.Error) {
	room, e := s.goalRoom(ctx, caller, req.Room)
	if e != nil {
		return contract.GoalListResponse{}, e
	}
	goals, err := s.store.ListGoals(ctx, room, req)
	if err != nil {
		return contract.GoalListResponse{}, failure(err)
	}
	return contract.GoalListResponse{Goals: goals}, nil
}

func (s *Service) GoalItem(ctx context.Context, caller contract.Principal, req contract.GoalItemRequest) (contract.GoalItemResponse, *contract.Error) {
	switch req.State {
	case "", contract.GoalItemOpen, contract.GoalItemDone, contract.GoalItemDropped:
	default:
		return contract.GoalItemResponse{}, invalid("state must be open, done or dropped")
	}
	if e := checkGoalText(req.Text); e != nil {
		return contract.GoalItemResponse{}, e
	}
	room, e := s.goalRoom(ctx, caller, req.Room)
	if e != nil {
		return contract.GoalItemResponse{}, e
	}
	event, err := s.event(room, caller, contract.GoalItemChanged{})
	if err != nil {
		return contract.GoalItemResponse{}, failure(err)
	}
	item, err := s.store.SetGoalItem(ctx, req, event)
	if err != nil {
		return contract.GoalItemResponse{}, goalProblem(err)
	}
	return contract.GoalItemResponse{Item: item}, nil
}

func (s *Service) GoalDone(ctx context.Context, caller contract.Principal, req contract.GoalDoneRequest) (contract.GoalDoneResponse, *contract.Error) {
	if req.Summary == "" {
		return contract.GoalDoneResponse{}, invalid("summary is required")
	}
	if e := checkGoalText(req.Summary); e != nil {
		return contract.GoalDoneResponse{}, e
	}
	room, e := s.goalRoom(ctx, caller, req.Room)
	if e != nil {
		return contract.GoalDoneResponse{}, e
	}
	event, err := s.event(room, caller, contract.GoalEnded{})
	if err != nil {
		return contract.GoalDoneResponse{}, failure(err)
	}
	if err := s.store.EndGoal(ctx, req, event); err != nil {
		return contract.GoalDoneResponse{}, goalProblem(err)
	}
	return contract.GoalDoneResponse{}, nil
}

func (s *Service) GoalJournal(ctx context.Context, caller contract.Principal, req contract.GoalJournalRequest) (contract.GoalJournalResponse, *contract.Error) {
	if e := checkGoalText(req.Text); e != nil {
		return contract.GoalJournalResponse{}, e
	}
	room, e := s.goalRoom(ctx, caller, req.Room)
	if e != nil {
		return contract.GoalJournalResponse{}, e
	}
	if req.Text != "" {
		if err := s.store.AddGoalJournal(ctx, room, req.Goal, caller, req.Text, s.now()); err != nil {
			return contract.GoalJournalResponse{}, goalProblem(err)
		}
	}
	entries, err := s.store.GoalJournal(ctx, room, req.Goal)
	if err != nil {
		return contract.GoalJournalResponse{}, failure(err)
	}
	return contract.GoalJournalResponse{Entries: entries}, nil
}

// FailureNotices tells ancestors of sessions that just failed.
func (s *Service) FailureNotices(ctx context.Context) error {
	return s.store.SendFailureNotices(ctx, s.now(), s.engineEvent)
}

// Checkins runs one check-in pass. Each report wakes its reader, so the pass decides who pays for a model turn.
func (s *Service) Checkins(ctx context.Context) error {
	return s.store.SendCheckins(ctx, s.now(), s.engineEvent)
}

func (s *Service) engineEvent(room contract.RoomID, body contract.EventBody) (contract.Event, error) {
	return s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, body)
}
