package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"

	"dyna-agent/internal/sessionclose"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store"
	"dyna-agent/internal/worktrees"
)

// closeHeir follows stored parents, skipping closed ancestors, then uses the
// room's root session. Closing the root itself has no automatic successor.
func (m *Manager) closeHeir(info Info) (Info, error) {
	seen := map[string]bool{info.ID: true}
	for id := info.ParentSession; id != "" && !seen[id]; {
		seen[id] = true
		parent, err := m.store().ResolveSnapshot(id)
		if errors.Is(err, ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return Info{}, err
		}
		if parent.RoomID != info.RoomID {
			return Info{}, errors.New("parent belongs to another room")
		}
		if parent.Closed.IsZero() && !parent.Closing.Active() {
			return parent, nil
		}
		id = parent.ParentSession
	}
	root, ok, err := m.RootSession(info.RoomID)
	if err != nil {
		return Info{}, err
	}
	if ok && root.ID != info.ID && !root.Closing.Active() {
		return root, nil
	}
	return Info{}, nil
}

// errGoalNeedsRoot marks a held goal that no open agent can take. Root and the
// owner hold no goals, so a close refuses this goal with or without --force.
var errGoalNeedsRoot = errors.New("goal has no open agent to hold it")

// refuseGoalsForTopHeir refuses a close whose heir is root, or the owner when
// no ancestor is open. It reads the heir itself; use refuseGoalsForHeir when
// the caller already holds one.
func (m *Manager) refuseGoalsForTopHeir(info Info) error {
	heir, err := m.closeHeir(info)
	if err != nil {
		return err
	}
	return m.refuseGoalsForHeir(info, heir)
}

// refuseGoalsForHeir refuses held goals that heir cannot keep working. Root
// and the owner hold no goals, so such a goal would have nobody to work it.
// The message names each open goal with the commands that resolve it, and
// tells root to ask the owner when only the owner can unblock the work. A
// forced close refuses for the same reason.
func (m *Manager) refuseGoalsForHeir(info, heir Info) error {
	if heir.ID != "" && heir.Role != RoleRoot {
		return nil
	}
	held, err := m.goalOwned(map[string]Info{info.ID: info})
	if err != nil || len(held) == 0 {
		return nil
	}
	goals := make([]Responsibility, len(held))
	for i, o := range held {
		goals[i] = o.r
	}
	message := fmt.Sprintf("%s\nRoot and the owner hold no goals, so a forced close keeps the work here instead of handing it to them. If only the owner can unblock a goal, ask the owner first with dyna request question. Then run one command above for each goal, or hand it to an open session.", responsibilityError("close", goals))
	return newCloseRefusal(message, errGoalNeedsRoot, CloseReasonGoalNeedsAgent, []CloseAction{CloseActionRetry}, goals)
}

// handoffClose runs after the turn stops and new assignments are fenced off.
// Each move commits through its existing store, so a retry moves only work left.
// Every step reports a typed outcome, so a client draws what moved, what stayed
// and what failed without reading engine wording.
func (m *Manager) handoffClose(ctx context.Context, info, heir Info) ([]sessionclose.Outcome, error) {
	var summary []sessionclose.Outcome
	var errs []error
	if m.ClosePRs != nil {
		moved, err := m.ClosePRs(ctx, info, heir)
		summary = append(summary, moved...)
		if err != nil {
			errs = append(errs, err)
			// Each PR failure names its PR. Only a failure that names no work
			// adds the close-level row, so the result never shows "PR #0".
			if !failedOutcome(moved) {
				summary = append(summary, sessionclose.Outcome{Result: sessionclose.ResultFailed, Failure: sessionclose.FailureHandoff})
			}
		}
	}
	items, err := m.Responsibilities(ctx, info)
	if err != nil {
		return summary, errors.Join(append(errs, err)...)
	}
	for _, item := range items {
		switch item.Kind {
		case "workflow":
			err := worktrees.Migrate(store.DataDir())
			var moved bool
			if err == nil {
				moved, err = shells.DefaultStore().HandoffRunOwner(ctx, item.ID, info.ID, heir.ID, worktrees.HandoffRunTx)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("handoff workflow %s: %w", item.ID, err))
				summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityWorkflow, Result: sessionclose.ResultFailed, ID: item.ID, Failure: sessionclose.FailureHandoff})
			} else if moved {
				summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityWorkflow, Result: sessionclose.ResultMoved, ID: item.ID, To: heirAddress(heir)})
			}
		case "goal":
			if m.checkpoint != nil {
				m.checkpoint("close-before-goal-handoff")
			}
			// A goal moves only to a live agent. Root and the owner hold no
			// goals, so a goal that would reach them is refused, never assigned.
			if heir.ID == "" || heir.Role == RoleRoot {
				errs = append(errs, fmt.Errorf("%w: %s; ask the owner with dyna request question, then drop or finish it, or hand it to an open agent", errGoalNeedsRoot, item.ID))
				summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityGoal, Result: sessionclose.ResultFailed, ID: item.ID, Failure: sessionclose.FailureGoalNeedsAgent})
				continue
			}
			moved, err := m.goalStore().HandoffHolder(info.RoomID, item.ID, info.ID, heir.ID)
			if err != nil {
				errs = append(errs, err)
				summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityGoal, Result: sessionclose.ResultFailed, ID: item.ID, Failure: sessionclose.FailureHandoff})
			} else if moved {
				summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityGoal, Result: sessionclose.ResultMoved, ID: item.ID, To: heirAddress(heir)})
			}
		case "item":
			summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityItem, Result: sessionclose.ResultReturned, ID: item.ID})
		case "question":
			summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityQuestion, Result: sessionclose.ResultWithdrawn, ID: item.ID})
		case "shell":
			summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityShell, Result: sessionclose.ResultStopped, ID: item.ID})
		}
	}
	return summary, errors.Join(errs...)
}

// failedOutcome reports whether a list already names a failed step, so the
// close does not repeat it with no work to name.
func failedOutcome(outcomes []sessionclose.Outcome) bool {
	for _, outcome := range outcomes {
		if outcome.Result == sessionclose.ResultFailed {
			return true
		}
	}
	return false
}

// heirAddress is the session that takes handed-over work, or empty when the
// work became top level. An empty heir has no address to name: Address alone
// would give a bare colon.
func heirAddress(heir Info) string {
	if heir.ID == "" {
		return ""
	}
	return heir.Address()
}

// closeJournalLines records the typed outcomes as the journal's log text, so a
// stored session history stays readable without a migration.
func closeJournalLines(outcomes []sessionclose.Outcome) []string {
	lines := make([]string, len(outcomes))
	for i, outcome := range outcomes {
		lines[i] = outcome.Text()
	}
	return lines
}
