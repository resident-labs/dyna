package watch

import (
	"fmt"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/changepolicy"
)

// SetRoomCIBudget changes only budget fields under the policy store lock.
func (s *Service) SetRoomCIBudget(roomID string, actor authority.Actor, budget ChangePolicy) error {
	if err := authority.Check("SetRoomCIBudget", actor, nil); err != nil {
		return err
	}
	if roomID == "" {
		return fmt.Errorf("room is required")
	}
	return s.Store.update(func(st *State) error {
		p := RoomPolicy(*st, roomID)
		previous := map[string]bool{}
		for _, id := range p.CITestAllow {
			previous[id] = true
		}
		notes := map[string]string{}
		for _, id := range budget.CITestAllow {
			note := strings.TrimSpace(budget.CITestAllowNotes[id])
			if note != "" {
				notes[id] = note
			}
			if !previous[id] && note == "" {
				return fmt.Errorf("add a measured time and reason for %s", id)
			}
		}
		// TODO: legacy/shim remove later — older app clients omit workflow exemptions; preserve them until all clients send this field.
		if budget.CIBudgetExempt != nil {
			p.CIBudgetExempt = append([]string{}, budget.CIBudgetExempt...)
		}
		// TODO: legacy/shim remove later — older budget clients omit the mode; preserve the saved choice until all clients send ciTimeBudget.
		if budget.CITimeBudget != "" {
			p.CITimeBudget = budget.CITimeBudget
		}
		p.CIWarnAfter, p.CIFailAfter = budget.CIWarnAfter, budget.CIFailAfter
		p.CITestMax = budget.CITestMax
		p.CITestAllow = append([]string{}, budget.CITestAllow...)
		p.CITestAllowNotes = notes
		p.CINoJUnitWarning = budget.CINoJUnitWarning
		if err := changepolicy.Validate(p); err != nil {
			return err
		}
		if st.Policies == nil {
			st.Policies = map[string]ChangePolicy{}
		}
		st.Policies[roomID] = p
		return nil
	})
}
