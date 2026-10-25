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

Closing transfers PR authorship and held goals to the nearest open ancestor.
It releases reviewer roles, cancels live workflows, and reports the handoff.
Root and the owner hold no goals, so close is refused when only they are left above
the session, and a forced close does not move the goals to the owner. Root asks the
owner about the blocker with `dyna ask` when only the owner can clear it, then the
goal is finished, dropped, or handed to an open agent.
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

## Instructions and their changes

A session's standing instructions come from one snapshot. The first turn builds
it from live state, and later turns receive the same bytes until a refresh, so
the cached prompt prefix holds. The snapshot holds:

- Dyna's session rules, and the worker context with the identity and depth.
- The global rules and the room rules.
- The subagent profiles the session may use.
- The room's feature index, from its library map.
- The background shell jobs this session started that still run.
- The open questions and ringing calls, for root only.
- The service contract, the consolidation charter, and the goal and brief.

When live state moves on, Dyna posts an `[DYNA INSTRUCTIONS CHANGED]` note to
the session. The note names only the sections that differ from what the session
was last told, with the lines added or removed. A rule, the profile list, the
feature index, a running job, the open questions and the goal or brief can
change this way.

The note never wakes the session. A running turn receives it as steering, and
an idle session reads it at the start of its next turn. The note comes from the
engine, so it asks for no reply.

An added or edited room rule reaches every open session in the room. Each
session reads its own note on its next turn, and the room gets one quiet row
that lists the sessions it reached. Do not post a new rule again to each child.

A quiet post differs from a waking post. A coordination post wakes an idle or
dormant session, and the post becomes its next turn. A quiet notice, such as an
instruction note, a shared change summary or an engine report, waits unread for
a turn that starts for another reason. Root reads a short digest of quiet posts
with the input of its next turn that has posts.

A parent still tells its children what a snapshot cannot. A new task, a changed
scope, a priority, an answer or a correction is a message: post it. The snapshot
carries standing state only, and only while the session is open. To correct a
child's goal, use `dyna goal edit <id> --amend "<text>"`: the correction joins
its instructions and also arrives as a post.

A compaction or a model transfer does not lose the instructions. After a
compaction, the next turn sends the current snapshot again, so the rules, the
goal and brief, the open questions and the running jobs return. The note
boundary also resets, so a later change is reported once. A transfer starts a
new harness conversation: the incoming model reads the same fresh snapshot and
the handoff packet, which names the saved work, the open work, the worktree and
the branch. It does not read the old conversation.

## Services

Dyna runs some sessions for itself: the librarian, the PR reviewers and Voice.
They are services, not members of the room's hierarchy. A service has no parent,
holds no goal and owns no place in the tree, so it never appears in
`dyna room members`, a parent check-in, the Children section of a session's
threads or an `@children` selector. It appears only with `dyna services`:
`list`, `show <service>`, `enable|disable <service>`,
`profile <service> <profile|auto>`, `restart <service>` and `log <service>`.

Change a service's profile with `dyna services profile` only, and never as part
of a bulk move of sessions: a service keeps the profile chosen for it. A failed
service turn posts one alert that names the service, its turn and its error.
Never manage a service with `dyna subagent transfer` or a raw settings key.
