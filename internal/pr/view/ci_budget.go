package view

import (
	"context"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/watch"
	"maps"
)

func (s *Service) SetRoomCIBudget(ctx context.Context, roomRef string, budget CIBudgetSettings) error {
	room, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	actor, err := authority.ActorIn(ctx, room.ID)
	if err != nil {
		return err
	}
	if err = s.Watcher().SetRoomCIBudget(room.ID, actor, watch.ChangePolicy{
		CITimeBudget: budget.CITimeBudget, CIBudgetExempt: budget.CIBudgetExempt, CIWarnAfter: budget.CIWarnAfter, CIFailAfter: budget.CIFailAfter,
		CITestMax: budget.CITestMax, CITestAllow: budget.CITestAllow,
		CITestAllowNotes: budget.CITestAllowNotes, CINoJUnitWarning: budget.CINoJUnitWarning,
	}); err != nil {
		return err
	}
	return nil
}

// CIBudgetSettings contains the room's pull request run and test limits.
// A nil no-JUnit warning setting uses the default, which is true.
type CIBudgetSettings struct {
	CITimeBudget     string            `json:"ciTimeBudget"`
	CIBudgetExempt   []string          `json:"ciBudgetExempt"`
	CIWarnAfter      string            `json:"ciWarnAfter"`
	CIFailAfter      string            `json:"ciFailAfter"`
	CITestMax        string            `json:"ciTestMax"`
	CITestAllowNotes map[string]string `json:"ciTestAllowNotes"`
	CITestAllow      []string          `json:"ciTestAllow"`
	CINoJUnitWarning *bool             `json:"ciNoJUnitWarning"`
}

func (s *Service) RoomCIBudget(roomID string) (CIBudgetSettings, error) {
	p, err := s.RoomChangePolicy(roomID)
	if err != nil {
		return CIBudgetSettings{}, err
	}
	warn, fail, err := p.CIBudget()
	if err != nil {
		return CIBudgetSettings{}, err
	}
	max, _, err := p.TestBudget()
	if err != nil {
		return CIBudgetSettings{}, err
	}
	warning := p.WarnNoJUnit()
	return CIBudgetSettings{CITimeBudget: p.CITimeBudget, CIBudgetExempt: append([]string{}, p.CIBudgetExempt...), CIWarnAfter: warn.String(), CIFailAfter: fail.String(), CITestMax: max.String(), CITestAllow: append([]string{}, p.CITestAllow...), CITestAllowNotes: maps.Clone(p.CITestAllowNotes), CINoJUnitWarning: &warning}, nil
}
