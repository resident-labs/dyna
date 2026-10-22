// Package steward assigns failed merge batches to their room's service session.
package steward

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/subagent"
)

type Service struct {
	Sessions Sessions
	Root     string
	Now      func() time.Time
}

func (s Service) Profile(pinned string) (string, string, error) {
	if s.Sessions == nil {
		return "", "", fmt.Errorf("merge steward needs a session manager")
	}
	profiles, err := s.Sessions.Profiles()
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	failures, err := s.Sessions.Failures(now)
	if err != nil {
		return "", "", err
	}
	return rolesession.ChooseProfile(profiles, pinned, failures, now, subagent.RoleSteward)
}

// Assign checks saved turn prompts under the role lock before dispatching. This
// repairs a restart between session assignment and the batch's notified write.
func (s Service) Assign(ctx context.Context, roomID, dir, pinned, key, report string) error {
	if s.Sessions == nil {
		return fmt.Errorf("merge steward needs a session manager")
	}
	lock, err := rolesession.Lock(s.Root, subagent.RoleSteward, roomID)
	if err != nil {
		return err
	}
	defer rolesession.Unlock(lock)
	lookup := rolesession.Lookup("", roomID, subagent.RoleSteward, s.Sessions.Inspect, func() (subagent.Info, error) { return s.Sessions.Named(roomID) })
	marker := "[MERGE BATCH " + key + "]"
	if lookup.Usable {
		for n := lookup.Info.Turns; n > 0; n-- {
			turn, err := s.Sessions.Turn(lookup.Info.ID, n)
			if err != nil {
				return err
			}
			if strings.Contains(turn.Prompt, marker) {
				return nil
			}
		}
		if rolesession.Busy(lookup.Info) {
			return fmt.Errorf("merge steward is busy with another assignment")
		}
	}
	name, _, err := s.Profile(pinned)
	if err != nil {
		return err
	}
	task := marker + "\n" + Playbook + "\n\n" + report
	if lookup.Usable && lookup.Info.Profile == name && !rolesession.NeedsRecreation(lookup.Info, lookup.Turn) {
		_, err = s.Sessions.Assign(ctx, lookup.Info.ID, task)
		return err
	}
	if lookup.Usable {
		if err := s.Sessions.Close(ctx, lookup.Info.ID); err != nil {
			return err
		}
	}
	_, _, err = s.Sessions.Start(roomID, name, dir, "Resolve failed merge batches safely and record each decision.", task)
	return err
}

const Playbook = `You are this room's merge steward. Own this failed batch until recovery is complete.
Read dyna git merge status for the failed batch, failing jobs, test excerpts, and logs.
Classify each failure. For a real PR defect, run dyna git merge return <pr> "<exact test and reason>".
For a suspect flaky test, create a scratch worktree with dyna git worktrees new at origin/main.
Re-run only that test on current main: go test -run '^Name$' -count=5 <pkg>, or the single web test.
Unset every DYNA_* variable for Go tests. If it fails on main, file dyna git issue --at <file:line> with the exact test, evidence, and area owner.
If the failure is unclear, run dyna git merge bisect once, or retry --without <pr> to isolate a suspect.
Use dyna git merge retry, retry --without, bisect, publish <trial>, or resume to finish recovery.
Never edit PR code. Never publish an untested commit. Never resume while main is red.
Record one decision line per action with dyna git merge note "<action, PRs, exact test and evidence>".
These notes reach root quietly and enter merge history. Send every other report through this quiet decision note.
Wake root with dyna post @root --alert only when main is red or you cannot decide after one bisect.`
