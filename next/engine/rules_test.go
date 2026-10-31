package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestRoomRules(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	var root contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&root); err != nil {
		t.Fatal(err)
	}
	first, problem := s.RuleAdd(ctx, owner, contract.RuleAddRequest{Room: room, Text: "First"})
	if problem != nil {
		t.Fatal(problem)
	}
	second, problem := s.RuleAdd(ctx, owner, contract.RuleAddRequest{Room: room, Text: "Second"})
	if problem != nil {
		t.Fatal(problem)
	}
	if first.Number != 1 || second.Number != 2 {
		t.Fatalf("numbers: %d, %d", first.Number, second.Number)
	}
	if _, problem := s.RuleAdd(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.RuleAddRequest{Room: room, Text: "Forbidden"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("worker add: %v", problem)
	}
	if _, problem := s.RuleRemove(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.RuleRemoveRequest{Room: room, Number: first.Number}); problem != nil {
		t.Fatal(problem)
	}
	list, problem := s.RuleList(ctx, owner, contract.RuleListRequest{Room: room})
	if problem != nil || len(list.Rules) != 1 || list.Rules[0].Text != "Second" {
		t.Fatalf("list: %+v, %v", list, problem)
	}
	var deliveries, wakes, turns int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(wake),0) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id=? AND e.type IN ('rule.added','rule.removed')", worker).Scan(&deliveries, &wakes); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*) FROM turns WHERE session=?", worker).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if deliveries != 3 || wakes != 0 || turns != 0 {
		t.Fatalf("deliveries %d, wakes %d, turns %d", deliveries, wakes, turns)
	}
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"rule.added", "rule.removed"})
	if err != nil || len(events) != 3 {
		t.Fatalf("rule events: %v, %v", events, err)
	}
	if _, problem := s.RuleRemove(ctx, owner, contract.RuleRemoveRequest{Room: room, Number: first.Number}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("remove missing rule: %v", problem)
	}
}
