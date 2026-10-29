# Librarian

The librarian is one long-lived agent session per repository. It keeps a
feature map, lints it, files architectural smells, and corrects false
statements in `README.md` and `docs/`. See issue #1164.

## Turn it on

```sh
dyna library enable --repo /path/to/main/checkout --room <room>
```

Only the owner or root can run `enable` and `disable`. `enable` adds the
repository to `library.enabled` and records the room that holds the session.
It adds one repository hook, `librarian-main`, on `main-updated`. The hook
runs `dyna library wake`. A merge always moves main, so one hook covers it.

When a wake must wait for the librarian's interval, the engine keeps that
wake itself. It runs one in-process timer per repository and fires the wake
when the interval ends. No hook or schedule runs it.

When the engine starts, it removes the `librarian-merged` and `librarian-batch`
hooks that earlier releases added. Each one ran `dyna library wake`.

`dyna library disable` removes the hook and closes the session. The map stays.

## When it runs

`dyna library wake` stops at the first check that fails:

1. The repository is in `library.enabled`.
2. The last librarian turn has finished. A successful turn moves the checkpoint
   to its commit. A failed turn is retried from the last checkpoint.
3. Main moved since the checkpoint.
4. 30 minutes passed since the last run. The engine's own timer runs the
   deferred wake when this interval ends.
5. The gate finds an unmapped source file, or an owned file whose change is
   more than whitespace. Otherwise the checkpoint moves and no turn runs.
6. The session is idle.

The session works in a checkout that Dyna owns and resets to main before each
turn. A compaction or a transfer loses nothing, because the map is the durable
state.

## The map

The map lives in `dyna.db`, not in the repository. Read it with
`dyna library list --tree`, `show <id>` and `changed --since <sha>`, or in the
Library view of the app. Only the repository's librarian session changes it,
through `add`, `update`, `link` and `retire`. The owner can also change it from
a shell.

`dyna library lint` reports warnings only:

1. A file owned by two entries that are not related.
2. A source file owned by no entry.
3. An entry that owns nothing, or a glob that matches nothing.
4. A dead link, or a backticked repository path in the README or `docs/` that
   no longer exists.
5. A summary that is too long, or a missing `differs` line.
6. An entry whose owned files changed since it was verified.
7. A missing parent, or a parent cycle.
9. A `README.md` longer than `library.readmeMaxLines`.

## Smells

`dyna library smell` files a smell on the room issue board. Dyna refuses a smell
without regular files in the repository, a smell that matches an open issue,
and more than `library.smellsPerDay` smells a day for one repository (default 10; 0 means no cap).

The owner or a room's root can start the smell pass at once with
`dyna library wake --smells --now [--repo path | --room name]`. It skips the
6-hour smell interval and has no daily cap, but it still holds the lock, waits
for a busy smell session, and refuses duplicate or missing-file smells. Any other
caller gets an error.

## Docs

In maintain mode, the librarian corrects statements that are now false and
leaves the rest alone. It edits only in its own worktree and opens a tracked
pull request. The assigned reviewer and CI check it, and it never merges.
Before it opens the pull request, it runs `dyna library check-readme`. When
`README.md` is still longer than `library.readmeMaxLines`, it does not open
the pull request.

## Settings

- `library.enabled`: JSON list of main checkout paths. Use `enable` and `disable`.
- `library.profile`: the default profile for librarian work when a room has no pin. Empty ranks service profiles.
  A room pin overrides it. Set one with `dyna services profile library <profile|auto> [--room R]`.
  The owner sets every room with `--all-rooms`. A room's root may set only its own room.
  Never change it as part of a bulk move of sessions: the librarian is a service, not a worker.
- `library.smellsPerDay`: the most smells automatic passes file for one repository in a UTC day (default 10, 0 for no cap).
- `library.readmeMaxLines`: the longest README the librarian may leave (default 300).
