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
	if budget.CITimeBudget != "warn" && budget.CITimeBudget != "enforce" {
		return fmt.Errorf("CI time budget must be enforce or warn")
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
		p.CIBudgetExempt = append([]string{}, budget.CIBudgetExempt...)
		p.CITimeBudget = budget.CITimeBudget
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
