# Repository lifecycle hooks

Hooks run trusted shell commands on Dyna events.
Dyna records who added each hook and when.
Hooks belong to the repository, so every room on that repository shares them.
The owner adds, edits, runs and removes them in Room settings, under Hooks, or
from a terminal with `dyna git hooks`.

See `dyna guide git`.

A hook contains `name`, `event`, `command`, `cwd`, `slot`, `timeout`, `onFail`,
`addedBy`, and `addedAt`. Edits change only the supplied fields, preserve the
creation records, and record `editedBy` and `editedAt`. Schedule hooks also contain `everyMinutes`.
Timeouts use duration strings, such as `30s` or `5m`.
`cwd` accepts `worktree`, `main`, or an absolute directory path.
`slot` accepts `light` or `heavy`. Heavy hooks share the checks runner's slots.
The timeout includes the slot wait and command run.

Events are `worktree-created`, `before-land`, `after-land`, `pr-merged`,
`main-updated`, `session-finish`, and `schedule`.
Only `before-land` allows `onFail: block`. A failed blocking hook prevents publication.
Before-land hooks run against the trial merge and receive its SHA.
Detached alert hooks get a separate trial worktree, which Dyna removes after the run.
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

The self-update recipe below is opt-in. It changes no existing timer.

If a before-land worker is killed, its temporary worktree can remain registered.
Remove that tree with `git worktree remove --force <path>` when no hook uses it.
If the temporary directory is already gone, `git worktree prune` clears its entry.


## Update Dyna

The `self-update` recipe has the label **Update Dyna**. It only accepts a Dyna
source repository. Enable it with this command:

```sh
dyna git hooks add --recipe self-update --repo /home/user/dyna-agent
```

Dyna does not add this hook automatically. The existing timer remains active.
The hook uses `main-updated`, which fires after origin/main gets new commits,
regardless of who merged them. Stored `main-moved` events are migrated to
`main-updated`; CLI input accepts only `main-updated`.

The recipe starts a detached user service named `dyna-install-<sha>`. Its work
continues when it restarts `dyna-engine`. The unit gets a shared heavy check slot
and waits for `hooks/install/install.lock` in Dyna's data directory.
After taking the lock, it fetches and re-reads origin/main. A queued older unit
therefore cannot install an older commit after a newer unit finishes.

The worker fast-forwards the main checkout only when it is on main and has no
tracked edits. A different branch, tracked edits, or local commits cause one
owner notice per cause. It stores the last warning cause in SQLite.
These checkout warnings do not stop a build in the separate autoinstall tree.

The worker uses `/home/user/dyna-agent-autoinstall` for this checkout. It runs
frontend `npm ci` and `npm run build`, Go build, and `desktop/build.sh` in order.
At add time, the recipe resolves Go, npm, and Node on PATH and captures the build
tool search path, without duplicates or temporary agent-computer bins under
Dyna's data directory `computers/` folder. It fails if a tool is missing. It backs up each
installed binary as `.bak-<time>`, keeps five backups, and replaces `dyna` and
`dyna-desktop` in `~/.local/bin`. The shared service installer writes both engine and computer units. The engine
restarts after each update. The computer service restarts only when the fixed
computer-code paths in `internal/hookinstall/computer_paths_gen.go` changed since the installed
commit. An active transient computer service is preserved until it stops and is
collected. A computer-service restart ends its desktops and apps.
After a successful service restart, it saves the installed commit in the
`hook_install_state` table in `dyna.db`.

The hook log records the unit name. Its result stays `pending` while the unit
runs. Durable receipts live in the `hook_install_jobs` table in `dyna.db`.
Output logs remain under `hooks/install` in Dyna's data directory.
The maintenance loop and `dyna git hooks log` copy the worker output and final unit
result into the original hook log. This also recovers a result after a server
restart. Completed units are stopped and cleared after that result is recorded. Finished
receipts are marked as archived in SQLite so each minute checks only active jobs.

The recipe builds origin/main, as the timer does.
The independent unit has a 75-minute limit, including slot and lock waits.
A second run for the same SHA fails fast while its earlier unit is still loaded.
Run `dyna git hooks log self-update` to reconcile and clear a completed unit.
