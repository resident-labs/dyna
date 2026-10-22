package roomsvc

import (
	"context"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
)

// rulesScope resolves the room and the rule scope: the room, or global rules.
// Global changes still post their notice in the room.
func (s *Service) rulesScope(roomRef string, global bool) (room.Record, string, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return r, "", err
	}
	if global {
		return r, goals.Global, nil
	}
	return r, r.ID, nil
}
func (s *Service) Rules(roomRef string, global bool) (goals.View, error) {
	_, scope, err := s.rulesScope(roomRef, global)
	if err != nil {
		return goals.View{}, err
	}
	return s.goalService().Store.RulesView(scope)
}
func (s *Service) AddRule(ctx context.Context, roomRef string, global bool, text string) (goals.Rule, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Rule{}, err
	}
	scope := r.ID
	if global {
		scope = goals.Global
	}
	rule, err := s.goalService().Store.AddRule(scope, author.ID, author.Name, text)
	return rule, s.ruleNotice(ctx, r, scope, rule, author.Address, "added", err)
}
func (s *Service) EditRule(ctx context.Context, roomRef string, global bool, id, text string) (goals.Rule, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Rule{}, err
	}
	scope := r.ID
	if global {
		scope = goals.Global
	}
	rule, err := s.goalService().Store.EditRule(scope, id, author.ID, ruleAuthority(ctx), text)
	return rule, s.ruleNotice(ctx, r, scope, rule, author.Address, "edited", err)
}
func (s *Service) RemoveRule(ctx context.Context, roomRef string, global bool, id string) (goals.Rule, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Rule{}, err
	}
	scope := r.ID
	if global {
		scope = goals.Global
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
	if err := goals.PostRuleNotice(s.rooms(), r, rule, who, verb); err != nil {
		log.Printf("rule %s was saved, but its room notice failed: %v", rule.ID, err)
	}
	s.Notify.Sessions.NoteRoomInstructions(ctx, goals.RoomOfScope(scope))
	return nil
}
