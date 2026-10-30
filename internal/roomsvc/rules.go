package roomsvc

import (
	"context"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
)

// Rules lists one rule set: the room's, or global rules. Global rules need
// no room, so Settings can show them with no room open.
func (s *Service) Rules(roomRef string, global bool) (goals.View, error) {
	if global {
		return s.goalService().Store.RulesView(goals.Global)
	}
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return goals.View{}, err
	}
	return s.goalService().Store.RulesView(r.ID)
}

// ruleCaller resolves the caller, its room and the rule scope. A global
// change may name no room; it then posts no room notice.
func (s *Service) ruleCaller(ctx context.Context, roomRef string, global bool) (room.Record, goalAuthor, string, error) {
	r, author, err := s.goalCallerIn(ctx, roomRef, global)
	if global {
		return r, author, goals.Global, err
	}
	return r, author, r.ID, err
}
func (s *Service) AddRule(ctx context.Context, roomRef string, global bool, text string) (goals.Rule, error) {
	r, author, scope, err := s.ruleCaller(ctx, roomRef, global)
	if err != nil {
		return goals.Rule{}, err
	}
	rule, err := s.goalService().Store.AddRule(scope, author.ID, author.Name, text)
	return rule, s.ruleNotice(ctx, r, scope, rule, author.Address, "added", err)
}
func (s *Service) EditRule(ctx context.Context, roomRef string, global bool, id, text string) (goals.Rule, error) {
	r, author, scope, err := s.ruleCaller(ctx, roomRef, global)
	if err != nil {
		return goals.Rule{}, err
	}
	rule, err := s.goalService().Store.EditRule(scope, id, author.ID, ruleAuthority(ctx), text)
	return rule, s.ruleNotice(ctx, r, scope, rule, author.Address, "edited", err)
}
func (s *Service) RemoveRule(ctx context.Context, roomRef string, global bool, id string) (goals.Rule, error) {
	r, author, scope, err := s.ruleCaller(ctx, roomRef, global)
	if err != nil {
		return goals.Rule{}, err
	}
	rule, err := s.goalService().Store.RemoveRule(scope, id, ruleAuthority(ctx))
	return rule, s.ruleNotice(ctx, r, scope, rule, author.Address, "removed", err)
}

// ruleAuthority keeps owner-rule permission separate from the recorded author.
// The goal store grants owner permission only for its "owner" sentinel.
func ruleAuthority(ctx context.Context) string {
	switch authority.Of(ctx).Role {
	case authority.Owner, authority.Root:
		return "owner"
	default:
		return ""
	}
}

// ruleNotice posts the caller's saved change in the room and tells the
// affected sessions that their instructions changed. A failed notice is
// logged, not returned, because the rule is already saved.
func (s *Service) ruleNotice(ctx context.Context, r room.Record, scope string, rule goals.Rule, who, verb string, err error) error {
	if err != nil {
		return err
	}
	sessions := s.Notify.Sessions.NoteRoomInstructions(ctx, goals.RoomOfScope(scope))
	if r.ID == "" {
		return nil
	}
	if err := goals.PostRuleNotice(s.rooms(), r, rule, who, verb, sessions); err != nil {
		log.Printf("rule %s was saved, but its room notice failed: %v", rule.ID, err)
	}
	return nil
}
