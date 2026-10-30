# Repository lifecycle hooks

Hooks run trusted shell commands on Dyna events.
Dyna records who added each hook and when.
Hooks belong to the repository, so every room on that repository shares them.
The owner adds, edits, runs and removes them in Room settings, under Hooks, or
from a terminal with `dyna git hooks`.

See `dyna guide git`.

A hook contains `name`, `event`, `command`, `cwd`, `timeout`, `onFail`,
`addedBy`, and `addedAt`. Edits change only the supplied fields, preserve the
creation records, and record `editedBy` and `editedAt`. Schedule hooks also contain `everyMinutes`.
Timeouts use duration strings, such as `30s` or `5m`.
`cwd` accepts `worktree`, `main`, or an absolute directory path.
The timeout includes the command run.

Events are `worktree-created`, `before-land`, `after-land`, `pr-merged`,
`main-updated`, `session-finish`, `session-turn-ended`, and `schedule`.
`session-turn-ended` fires when a bound session's turn ends, so a job can react
to a turn at once instead of at its next interval.
Only `before-land` allows `onFail: block`. A failed blocking hook prevents publication.
Before-land hooks run against the trial merge and receive its SHA.
Detached alert hooks get a separate trial worktree, which Dyna removes after the run.
`worktree-created` hooks run in the foreground: worktree creation waits for each one and names any hook that fails.
Other events launch detached workers and do not wait for shell commands.

Hook commands receive these environment variables:

- `DYNA_HOOK_EVENT` names the event.
- `DYNA_HOOK_REPO` contains the shared repository identity.
- `DYNA_HOOK_WORKTREE` contains the event worktree path.
- `DYNA_HOOK_BRANCH` and `DYNA_HOOK_SHA` identify the event revision.
- `DYNA_HOOK_PR` identifies the pull request when available.
- `DYNA_HOOK_SESSION` identifies the session when available.

Unavailable values are empty. Commands do not inherit the emitter's session identity.
Hooks use the checks runner for command execution, timeouts, logs, and stored results.
Hook results cannot satisfy landing check requirements.
A failed alert hook sends one owner notice per cause class until the hook succeeds.
A later failure after a successful run sends a new notice.

Dyna stores hook configuration in the `hook_documents` table in `dyna.db`.
At startup, a one-time migration imports retained `repos/<safe-id>/hooks.json` files.
Later changes to retained files do not change hook configuration.
The safe ID is a SHA-256 digest of the shared remote identity.
Different remote transports for the same repository share hooks.
A repository without a remote uses its root commit hash.
Hook state and configuration stay outside the repository.

The maintenance loop checks schedules each minute. An interval runs at most once
per due check, without replaying missed intervals. Main hooks fetch origin/main
at most once every five minutes. The first observation sets the baseline.
Later SHA changes emit `main-updated`, including changes made outside Dyna.
The maintenance loop finds configured hooks even when no session remains.
It also wakes the librarian on main updates for each enabled library, without a hook.

If a before-land worker is killed, its temporary worktree can remain registered.
Remove that tree with `git worktree remove --force <path>` when no hook uses it.
If the temporary directory is already gone, `git worktree prune` clears its entry.


## Update Dyna from source

The Updates service can install Dyna from a source checkout. Set an absolute
checkout path and turn on the service:

```sh
dyna settings set updater.source /home/user/dyna-agent
dyna services on updates
dyna services set updates --interval 60m
```

The service runs when the configured checkout's default branch changes and at
its interval. The first branch observation sets a baseline. It uses the same
detached installer, logs and durable receipts as the former update hook. The
installer fetches the default branch, builds in a separate autoinstall checkout,
and restarts the engine after a successful install. Its 75-minute limit includes
the shared slot and install lock waits. The Services page shows its runs.

Release installations without a source setting keep the existing release update
path. A release installation with an existing source recipe keeps that explicit
source-update choice after migration. The migration leaves custom commands in
Hooks. To switch a custom update hook to the service, set `updater.source`,
disable the old hook with `dyna git hooks edit <name> --disabled`, and turn on
Updates. If several unmodified update recipes exist, Dyna leaves them all in
Hooks and asks the owner to choose a source. Disable the unwanted hooks before
turning on the service.
