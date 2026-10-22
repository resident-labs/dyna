# Git

Use `dyna git` for version-control work: `pr`, `change`, `ci`, `worktrees`, `hooks`, `sync`, and `issue`.
Use `dyna git worktrees new <branch>` for a managed checkout.
Run `eval "$(dyna git worktrees env)"` to set `DYNA_PORT`, `DYNA_PORTS` and `PORT` for that worktree's dev servers; open them with `dyna browser open http://127.0.0.1:$DYNA_PORT`.
Old top-level command names remain hidden for one release; use the git paths in new briefs and scripts.

## Pull requests and changes

After `gh pr create`, run `dyna git pr track <number|url>` from the matching checkout.
Elsewhere, add `--repo <path>`. Fix CI failures and push. The engine merges automatically when approved and green; agents never run merge commands.
`dyna git pr show <pr>` explains pauses; `--history` includes delivered notices.
`dyna git pr resume <pr>` clears a CI wake pause.

DYNA_MODE=subagent with DYNA_SESSION identifies an acting session. An external
shell acts as the owner. Workflow workers keep their worker identity.
Owner and root can track, close, and reply. Commands use the caller's identity;
`dyna git pr` and `dyna git change` take no acting `--session` flag.
Room commands use `--room`, then the room stamp, then the current repo.
A stamped session cannot open a change in another room.
Owner tracking records no session author. Its PR notices go only to the owner;
CI and land notices never start an agent.

Keep authors open while their change is open and watched. Dyna closes reviewers
that have no live turn on a needed current head, including after a verdict.
Paused rooms start no new reviewers.
Use `dyna git pr transfer <pr> --role author --to <session>` to transfer authorship.
Use `dyna git pr reviewer` to inspect reviewers, `--profile <profile>` to pin one,
`--auto` for automatic choice, or `--restart` to restart them.
`dyna git pr review on|off` controls automatic review.
Only the assigned reviewer records an agent verdict. Reviewers may use up to
3 helpers on their profile or a cheaper one. Helpers cannot delegate.

## Open and review a change

A change is a committed branch with a base and an exact head. It needs no PR.
Create its checkout with `dyna git worktrees new <branch>`, never a `~` path.
From that checkout, run:

```sh
dyna git change open --base origin/main --intent "Fix parser bounds" \
  --testing "Focused tests passed" --risks "Input compatibility" \
  --questions "Check fallback" --category fix
```

Run it again after commits to update the change. Omitted notes remain.
`--pr <number|url>` attaches a PR and preserves existing findings and history.
`dyna git pr track` also opens a change. Use `dyna git change list`,
`dyna git change show <id>`, and `dyna git change close <id>` to manage changes.
Closing never merges or deletes the branch.

A reviewer starts on each needed verified head, in parallel with CI.
Draft PRs get no automatic review, even with required reviewers. Open a PR
ready for review, or run `gh pr ready <number>` when it should be reviewed.
Known failed CI blocks review until a rerun starts or passes.
Reviewers never run the test suite; CI is the test oracle. They read the diff
and may run cheap static checks on changed files (go vet, gofmt, tsc, linters).
Read the assigned head and review input. Confirm or reject every bot finding
with a reason after checking its file and line.
New PR comments steer the live reviewer. A pushed head gets a fresh reviewer
with the earlier findings and delta context. The final review answers each comment.
A reviewer may return changes or approve before CI finishes. Dyna marks a change
ready only when an approval and green CI are on the same head. CI failures go
to the author; the final review notes the CI result.
Source-only reviews of draft changes need no CI evidence. Merging a PR requires passing GitHub CI.

Verdicts are `approve`, `approve-with-nits`, `changes`, or `question`.
Approval cannot contain `blocking` or `should-fix` findings.
Findings JSON is an array with severity (`blocking`, `should-fix`, `nit`), file,
nonnegative line, why, fix, and optional patch and stable ID. Preserve existing IDs.

```sh
dyna git change verdict <id> changes --head <assigned-sha> \
  --body-file review.md --findings-file findings.json
dyna git change reply <id> <finding-id> fixed --message "Added bounds check"
```

Authors reply `fixed`, `wont-fix`, or `question`. Replies to changes or questions
queue review again. Reviewers check the delta and earlier findings and replies.
`dyna git pr verdict` accepts the same verdicts and findings for PRs.

## Publishing and comments

Agent reviews stay in Dyna by default. Read the assignment's publishing mode; it remains fixed for that assignment.
`dyna git pr publishing [local|github]` sets the mode for future assignments.
Publish only when the assignment enables it or the owner explicitly asks.
For local review, read GitHub without posting and save the complete review:

```sh
dyna git pr verdict <pr> changes --body-file review.md --head <assigned-sha>
```

Include findings, paths, lines, and fixes. `--body-file -` reads stdin.
Reviews must be at most 12000 bytes. Local verdicts require no GitHub review ID.
For GitHub mode, use a comment review because agents share the owner's account:

```sh
dyna git pr review <pr> --body-file review.md --comments-file inline.json --head <assigned-sha>
dyna git pr verdict <pr> approve --head <assigned-sha> --review-id <github-review-id>
```

The review ID must match that reviewer and head.
Post other GitHub comments with `dyna git pr comment <pr> --body "..."` so replies route correctly.
`--publish` explicitly publishes one comment when the room uses local mode.
Root can publish `dyna git pr comment` and `dyna git pr review --publish`.
These posts stay unattributed; posts and replies route as owner feedback.
Never add HTML markers or agent attribution to comments.
Mark bot spam with `dyna git pr noise <pr> <comment-id> --reason "why"`.
Use `dyna git pr noise list` to inspect rules and `dyna git pr noise rm <id>` to undo one.

## CI checks and budgets

Room policy chooses `warn` or `enforce` for CI time budgets.
Rooms default to `warn`: slow tests and jobs produce quiet warnings.
In `enforce` mode, runs warn after 2 minutes and fail after 3 minutes.
The room owner sets the mode with `dyna git change policy --ci-time-budget enforce`
or `dyna git change policy --ci-time-budget warn`.
Only runner step time counts; dispatch waits and reporting gaps do not count.
The budget follows the longest chain of dependent jobs, using their step times.
Push runs are never cancelled. Each attempt has its own budget.
Root can set the room budget:

```sh
dyna git pr budget
dyna git pr budget --warn 2m --fail 3m --test 10s
```

Keep each test under 10 seconds. Authors run the affected test commands directly in the agent environment under `dyna.slice`. For Go, test only touched packages. Keep `DYNA_OWNER_ENV` and `DYNA_BIN` set: they keep `dyna` on the owner's data folder. If a test reads the session stamp, run it with `env -u DYNA_SESSION -u DYNA_MODE`. A test that needs its own data folder calls `storetest.DataDir(t)`. No local evidence ledger is required; GitHub CI gates merges.
Export Go reports with `dyna git ci tests --junit-output <file>`.
Missing JUnit produces one warning; `dyna git pr budget --no-junit-warning=false` disables it.
`--allow package/TestName` sets exact allowed test names; repeat it for more names.
`--allow ''` clears the allowlist. Allowed tests still fail on execution errors.
`--exempt CodeQL,Images` excludes workflow names from timing; `--exempt ''` clears it.
Exempt workflows still gate CI. In enforce mode, runs that finish over budget still fail. Warning time must be positive and below failure time.

## Sync and local CI

Use `dyna git sync status --json` to inspect saved ahead and behind counts.
`dyna git sync fetch` refreshes the configured local lane remotes.

Local CI runs one commit over SSH. It excludes live computer tests.

```sh
dyna git ci run <sha>
dyna git ci run <sha> --suite go
dyna git ci show <sha> --json
```

Use `--suite relay,frontend,go,cross-build` to select suites.
The configured SSH destination is `localci.remote`.

## Hold and release

Use `dyna git pr hold <number|url> "<reason>"` to make a tracked PR a GitHub draft.
Dyna posts the reason to the PR's room. GitHub draft status prevents automatic merging and automatic review.
Use `dyna git pr release <number|url>` when the PR can merge again.
The room owner or current author can hold or release a PR.
Release removes the draft hold. The PR must still pass review and CI before it can merge.

## Automatic merging

Dyna merges tracked pull requests automatically when the current head has
a Dyna reviewer approval and passing GitHub CI. The engine runs one merge
at a time. Authors do not run a landing command or a merge command.
Root controls failed batches with `dyna git merge`; see Root merge controls.

Read `dyna git pr merge-status` for the queue and saved outcomes.
Open stack bases wait for their parent PR to merge. Dyna retargets children
before merging a base PR, so deleting that branch does not close its children.
Among eligible PRs, the oldest-ready PR goes first.
Use repeated `--depends-on <id>` and `--category` when opening a change, so the reviewer sees the order.

If GitHub reports a conflict, Dyna posts the author to rebase onto main.
Push the rebased head, then wait for its new review and CI.

`dyna git change policy` reads room defaults; only root changes them.
No stamp means root. Authors or root can override review or named GitHub check settings
with `--reason`. These settings control review work; automatic merging still
needs exact-head approval and green GitHub CI.

After each merge, the existing hook spool runs repository hooks.
The install service combines installs during a burst of merges.
Main push CI checks the merged result.

## Root merge controls

Dyna automatically merges approved PRs with passing CI. Successful batches
remain automatic. Per-PR review and CI feedback still goes to authors.

When a batch fails, Dyna pauses the queue and assigns the merge steward.
If the steward cannot start, root receives one waking notice instead.
Set merge.stewardProfile to off to skip the steward; failed batches then reach root directly.
The steward records decisions quietly for root's next turn and in merge history.
Authors continue to fix their own PRs.
Dyna does not automatically recover a passing prefix. Bisect runs on demand.

Root and the merge steward use these commands in their own stamped room:

```text
dyna git merge status
dyna git merge pause
dyna git merge resume
dyna git merge cancel
dyna git merge return <pr> "<reason>"
dyna git merge retry
dyna git merge retry --without <pr> --without <pr>
dyna git merge bisect
dyna git merge publish <trial>
dyna git merge note "<decision and evidence>"
```

`note` records one quiet decision line in merge history and root's next turn.
Root and the room's steward may record notes. Other workers cannot.

`status` prints JSON with the queue, current failed batch and pause state.
`pause` stops automatic merging for the room. `resume` clears that merge
pause; a failed frozen batch still needs a recovery action. These controls
do not change the room session pause.

`cancel` deletes the current batch and its trial branches. Its PRs return
to the existing queue. A merge pause remains until you resume or retry.
`return` sends a PR to its author with your reason and saved failure evidence.
That head stays out of batches until its author pushes a new head.

`retry` drops the old batch and creates a new batch from the current queue.
Repeat `--without` to exclude PRs from that retry batch. Use `return` to hold
a PR until its next head. A retry with no eligible PRs leaves merging paused.

`bisect` starts the existing bounded prefix trials for the failed batch.
That requested fallback may publish its passing prefix and return its failing
PR. `publish` publishes a saved passed trial by its branch or numeric suffix.
It checks current push CI, fresh PR gates, ancestry from current main, and the
main lease. It preserves the room merge pause, so you can inspect the result
before you resume batching.

Inspect status before choosing a recovery action. Return a PR when its author
must fix it. Retry after the cause is fixed, or exclude a PR from the retry.
Use bisect when you want the existing prefix recovery. Use publish when you
have chosen a saved passing trial. Resume when the queue can continue.

The service checks stored role and room authority for every control. Human root callers can
use `--room <name-or-id>` when they have no current stamped room.

## Repository hooks

Hooks run trusted shell commands. Worktrees share settings and logs outside the repo.
Read hooks before changing repository automation. Agents may change hooks; tell the owner with
`dyna post @parent --needs-root "..."`, explaining the command and purpose. Avoid one-off hooks.

Events: `worktree-created`, `before-land`, `after-land`, `pr-merged`, `main-updated`,
`session-finish`, and `schedule`. The merge service emits `pr-merged`.
The before-land and after-land events remain for stored hook configurations. `main-updated` is a generic repo event; its first observation sets a
baseline. Schedules skip missed intervals.

```sh
dyna git hooks list
dyna git hooks log verify --repo /path/to/repo
dyna git hooks add verify --event pr-merged --command 'go test ./...' --timeout 10m --on-fail alert
dyna git hooks edit verify --timeout 5m
dyna git hooks test verify
dyna git hooks run verify
dyna git hooks rm verify
```

`test` runs real commands with sample values and can change files or services.
`run` uses Git values. Timeouts bound each command. Edits preserve
unsupplied fields; removal preserves logs. Commands accept `--repo`; see help.

Merge hooks alert on failure. On failure, inspect `list` and the exact log from the notice,
fix the cause, then test before retrying. Tell the owner about hook changes.

## Issues for later

When you find an architectural smell outside your current task, file it with `dyna git issue`. Filing wakes nobody.

```bash
dyna git issue "Unify the parser paths" --at internal/parser.go:12 --note "Two callers use different rules."
dyna git issue list --json
```

Root or the owner can triage an issue with `dyna git issue close <id> --reason "..."`, `dyna git issue goal <id> [--for @name]`, or `dyna git issue export <id>`. Export uses the room GitHub account and requires read-write access.
