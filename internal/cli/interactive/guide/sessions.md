# Session lifecycle

Each session has one status. Use the status from the session record or member list.

| Status | Meaning |
| --- | --- |
| queued | A turn is active and prepares its prompt, or waits for admission. |
| running | A turn is active and executes. |
| idle | The session is open and has no active turn. It can still own goals, PRs, children, questions, shells or workflow runs. |
| dormant | The session ended a turn with no work left. It waits for a coordination post addressed to it. |
| closed | The session has ended. Posts cannot wake it. |

A normal turn moves from idle to queued, then running, then idle. A failed or stopped
turn also returns the open session to idle.

Closing transfers PR authorship and held goals to the nearest open ancestor, or root.
It releases reviewer roles, cancels live workflows, and reports the handoff.
A failed handoff keeps the session open unless force close is chosen.

A turn that ends with no responsibilities makes the session dormant. Its goals, PRs,
questions, children, shells and workflow runs must be done, dropped or moved first;
the shell job the turn runs in never counts. The session's own turn runner makes the
change after a successful turn.

A coordination post addressed to a dormant session starts a new turn. Informational
notices stay unread and never wake it. Idle time never makes a session dormant.

Maintenance closes a session after it stays dormant and inactive for 7 days
(`DormantCloseAfter`). It checks ownership and pending input again before closing.
The owner can also use Clear dormant or Dismiss for the same checks sooner.

Closed sessions stay closed until an explicit `dyna subagent reopen <session>`.
Reopen preserves the conversation and turn history and returns the session to idle.
It does not start a turn. The next coordination post can start one.
