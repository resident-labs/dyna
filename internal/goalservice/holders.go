package goalservice

import (
	"errors"
	"fmt"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/sessionlife"
)

// ResolveHolders uses Post's mention rules, with @room for a shared goal.
// Root holds no goals, so @root names no holder.
func (s *Service) ResolveHolders(roomID, by string, selectors []string) ([]string, bool, error) {
	if len(selectors) == 0 {
		if !sessionlife.IsSessionID(by) {
			return []string{}, true, nil
		}
		return []string{by}, false, nil
	}
	for _, sel := range selectors {
		if sel == "@root" {
			return nil, false, errors.New(goals.RootHoldsNothing)
		}
		if sel == "@room" {
			for _, other := range selectors {
				if other != "@room" {
					return nil, false, fmt.Errorf("choose @room or named holders")
				}
			}
			return []string{}, true, nil
		}
	}
	rec, err := s.Notify.Rooms.Resolve(roomID)
	if err != nil {
		return nil, false, err
	}
	sender := notify.Sender{Kind: notify.SenderOwner, Room: rec.Name}
	if sessionlife.IsSessionID(by) {
		info, err := s.Notify.Sessions.Store.Inspect(by)
		if err != nil {
			return nil, false, err
		}
		if info.RoomID != rec.ID {
			return nil, false, fmt.Errorf("use the current session's room")
		}
		sender.Kind, sender.Session = notify.SenderSession, info
	}
	sessions, err := s.Notify.ResolveSessions(sender, selectors)
	if err != nil {
		return nil, false, err
	}
	holders := make([]string, 0, len(sessions))
	for _, info := range sessions {
		if info.RoomID != rec.ID {
			return nil, false, fmt.Errorf("goal holders must be in this room")
		}
		holders = append(holders, info.ID)
	}
	return holders, false, nil
}
