# PR tracking and agent review

Dyna links a GitHub pull request to its author session. A host process uses
`gh` to check watched PRs every minute, including quiet PRs.
Sign in with `gh auth login` first.
Tracking is explicit because a shared branch does not identify which agent
opened a PR. The engine merges approved PRs with passing CI through batch
trials (see `dyna guide git`). Root can also publish a failed trial after naming
every failing test as flaky. Authors never run merge commands.

## Issue links

A tracked PR can link the issues that it fixes. The merge closes them; nobody
closes them by hand.

```sh
dyna git pr track 265 --fixes D-5,#207
dyna git change open --pr 265 --fixes D-5
dyna git pr show 265
```

A `Fixes D-5` or `Fixes #207` line in the PR body links the same issues. The
engine reads the body at track time and on later polls, and adds what it finds.
It never removes a link because a body line went away: run track with
`--fixes` to replace the list. A PR that is already merged when it is first
tracked closes nothing, because no watch saw the merge.

When the PR merges, Dyna closes each linked room issue with the reason
`Fixed by #N` and the merge commit on the close itself, and closes each linked
GitHub issue with a one-line comment that names the PR. GitHub also closes a
`Fixes #N` issue by itself, so a closed issue takes no second close. A PR that
closes without merging closes nothing. The PR row, the PR detail and the issue card show the
links: `Fixes D-5, #207` on the change, and `Fix in #N` on an open room issue.

## Goal links and expedited goals

A new change or tracked PR records its author's nearest active goal. Dyna
checks the author first, then each parent. It leaves the link empty when a
session has several active goals. Dyna does not add links to existing records
during an upgrade or a later tracking call.

Pass `--goal g12` to `dyna git pr track` or `dyna git change open` to choose an
open goal in the same room. The owner, root, author, or author's ancestor
can replace the link. Tracking again without `--goal` preserves the link,
including an empty link. The app can also link or clear a goal.

The owner or root can expedite one active goal per room:

```sh
dyna goal expedite g12
dyna goal expedite g14 --replace
dyna goal expedite --off
```

Expediting the same goal preserves its original expiry. Expediting a second
goal needs `--replace`. Priority ends when the goal is done or dropped, when
it is stopped, or after 24 hours. Goal reads and live events remove expired
priority. Goal holders and their descendants share the goal's priority.
PRs and changes use their saved goal link.

Linked changes get priority for the next available review slot in their room.
Eligible expedited PRs get an isolated merge trial, with merge priority per repository.
When normal work is waiting, it gets a slot after three expedited selections.
Review fairness is per room; merge fairness is per repository. Existing review,
dependency, draft, mergeability, and CI gates still apply. Running reviews, merge
trials, and CI continue. Expedite does not change session, workflow, or CI scheduling.

## Polling and GitHub rate limits

The engine worker loop ticks once a minute. Each PR has its own due time. First polls and
new heads run first. Other PRs run in order of their oldest completed poll,
including PRs with pending CI. Four snapshot workers share a 50-second tick
deadline. Each record has a 15-second deadline.

The watcher saves each request path's ETag, response body, and next-page link
in the PR store. It sends `If-None-Match` on the next request for that path.
A 304 response reuses the saved body and link. Each page has its own validator;
the watcher follows `Link` headers instead of using one `gh api --paginate`
validator for all pages. Cached paths for old heads are removed after a
successful poll. These headers come from `gh api --include`.

GitHub documents that authenticated 304 responses do not count against the
[primary rate limit](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests-if-appropriate).
Its [pull request](https://docs.github.com/en/rest/pulls/pulls#get-a-pull-request),
[review](https://docs.github.com/en/rest/pulls/reviews), and
[comment](https://docs.github.com/en/rest/issues/comments) docs do not promise
that every comment edit or review changes the pull's `updated_at`.
When CI is settled, an unchanged pull response (304, or the same timestamp
and head) skips CI, comments, logs, budgets, and JUnit artifacts for that tick.
This uses one metadata request. Pending CI, changed metadata, a changed policy,
and unfinished artifact reports require a full poll. Full polls still use
conditional requests for checks and all three comment lists. A full safety poll
runs about every ten minutes to catch comment edits and CI reruns that do not update pull metadata. Stable slots spread these
polls across ticks. New comments that update the pull timestamp trigger a full
poll at the next minute. Merge plans refresh unchanged bases every five minutes;
new candidates and local base changes still refresh at once.

A 403 or 429 rate-limit response pauses all watcher polls until the reset time
or `Retry-After`, whichever is later. A secondary limit without those headers
starts with a one-minute pause and increases the delay after repeated limits.
The engine logs a new pause once. It still delivers pending Dyna notices.
The shared pause survives engine restarts. `dyna git pr show` includes `rateLimit`
and `polling`, with the reset, pause end, remaining budget, and next due time.
`dyna git pr resume` clears the CI wake pause but does not bypass a GitHub pause.
When fewer than 500 requests remain, every PR uses a five-minute interval
until the budget resets or a later response shows at least 500 requests.
See GitHub's [rate-limit response rules](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api#exceeding-the-rate-limit),
[pagination rules](https://docs.github.com/en/rest/using-the-rest-api/using-pagination-in-the-rest-api),
and the [GitHub CLI flags](https://cli.github.com/manual/gh_api).

See `dyna guide git`.

`show` returns JSON with the head, CI state, consecutive failure count,
`pauseReason`, reviewer assignment, verdict, last poll error, comment filter
decisions, and pending notifications with full bodies and delivery errors.
`deliveredNotifications` counts delivered notices. Their bodies stay hidden
unless you add `--history`. `--json` uses the same output shape and history
flag. Showing a PR does not change its stored notices. Poll errors do not end watching.
No checks, or unfinished checks, mean CI is pending. A failed CI attempt sends
one batch with failed job names and a capped log tail. Repeated polls do not
count again. After three consecutive failed attempts, Dyna pauses further CI
wakes and escalates once (see [Escalation](#escalation)).
It does not stop the author's active turn. Passing CI or
`resume` clears that pause. New owner comments still wake a CI-paused author.

## Keep linked sessions open

See `dyna guide git`.

## Escalation

Author notices, "ready to land" notices and the third-CI-failure notice use
one rule, shared with stalled goals (`internal/escalate`):

1. Walk from the author up its parent chain. The first open session gets the
   notice. Idle and dormant (finished) sessions count as open, because a
   message wakes them. Closed sessions are skipped.
2. A "ready to land" notice skips an author that has a parent session, because
   a child author was told not to merge. A top-level author gets its own PR.
3. If nobody in the chain is open, a CI or land notice starts one agent in the
   room, with the brief "Fix CI on PR #N" or "Land or close PR #N". Dyna makes
   it the author. Other notices go to the owner. It uses the author's profile when
   that profile is enabled and not cooling down, else the default profile.
   Dyna starts at most one agent per PR head.
4. The owner receives a CI or land notice only when no agent can start.

Approvals still go to the owner.

Merged and closed PRs send one outcome notice per PR and outcome, including
merges published by the engine. These wake the author so it can mark its goal
done, clean up its worktree, and end its turn. If the author is closed, the notice
walks up its parent chain to the nearest open ancestor. If no recipient is open,
the owner receives it; ended changes never start an agent. Ready-to-land routing
stays the same. The separate worktree cleanup notice stays quiet.

## Require extra reviews

The PR author, an ancestor of the author, or the room root can add required
reviewers. The owner can also add them:

```sh
dyna git pr require 123 thoroughly-idiomatic perf-lead
```

Names resolve to open sessions in the PR's room. Requirements are additive;
adding the same session again changes nothing. The author cannot require its
own approval. Existing room review settings and the normal review pool remain.

Each required session records its verdict with `dyna git pr verdict` and
`--head <current-head>`. It needs no pool assignment. The merge queue waits
until every required session has approved that exact head. A new push makes
earlier approvals stop counting, and each required session that still owes a
verdict is re-assigned at the new head, so a dormant reviewer is woken instead
of silently stalling the PR; a head that leaves and returns restores an
assignment that was not yet delivered. A GitHub draft holds these assignments
like it holds the review pool, and releasing the draft delivers them at the
current head without another push. `dyna git merge status` and the PR page
show the missing reviewers. Releasing a draft does not bypass this review
gate.

A required reviewer can be removed the same way it was added. Closing a
reviewer's session releases its requirement on its own; every other case,
including a dormant reviewer, uses:

```sh
dyna git pr unrequire 123 thoroughly-idiomatic
```

An open reviewer resolves by room name. A closed reviewer resolves only by
the session ID that `dyna git pr show` lists. Removing a session that is not
required changes nothing.

## Choose the reviewer

See `dyna guide git`.

Dyna derives the review queue from current PR and session facts. A watched,
open, non-draft change needs review when its head is verified, CI has not
failed, and that head has no verdict. Review policy and the room pause also
apply. A draft PR gets no automatic reviewer, even with required reviewers;
mark it ready for review to start one.
A paused room starts no reviewers and makes no GitHub polling requests.
Polling resumes when the room resumes.

Only a live reviewer turn on a needed current head occupies pool capacity.
Dyna closes other reviewers, including idle turns, completed verdict holders,
old heads and ended changes. It assigns the oldest waiting changes first.
The chosen profile's max-concurrent limit sets the cap; the default is 3.
A dependent change waits until its open base is approved. Restart closes
the room's reviewers and assigns every still-needed head again.

Queue positions count waiting changes only. The status lists live reviewers,
their profiles and current reviews. The old top-level session and current
fields show the first live reviewer. Reviewer sessions and helpers do not
appear in the normal session lists. The change view can open a live reviewer.

A reviewer can work while CI is pending and record a verdict before CI ends.
Known failed CI blocks new review work until a rerun starts or passes.
A new head gets a fresh reviewer with the earlier findings and delta context.
New comments reach a live reviewer as steering. Reviewers never run the test
suite; CI supplies test results. They can run cheap static checks.
Dyna reports ready only when the verified head has approval and fresh green CI,
the PR is not a draft, GitHub confirms mergeability, and the base is its default
branch. An unknown default branch or mergeability result holds readiness.

Agent reviews stay in Dyna, including when a room retains a GitHub publishing
setting. New reviewer assignments always use `local`. Agents, including root
and workflow workers, cannot publish GitHub PR comments or reviews through
the CLI or engine service. Only the owner can publish GitHub feedback or
select the GitHub publishing setting. Existing GitHub feedback remains public.
The owner can admit held feedback with `dyna git pr admit <pr> <kind:id>`.

See `dyna guide git`.

A review over 12000 bytes is rejected. Accepted Markdown is preserved, with
secret redaction. Local verdicts require the assigned head and no GitHub review ID.
Dyna shows the review in the session and repository PR views.

## Comment identity and noise

Dyna records every GitHub comment ID it posts with the session that posted it.
Replies and ownership route by those stored IDs, and comments from a known
review belong to its reviewer.

See `dyna guide git`.

Dyna admits comments from the signed-in GitHub owner and authors whose
`author_association` is `OWNER`, `MEMBER`, or `COLLABORATOR`. It also admits
configured trusted bots. The default list is `coderabbitai` and
`copilot-pull-request-reviewer`. Set `pr.trustedBots` to a JSON list of bot
logins; an empty list trusts no bots.

All other comments are held in SQLite. They never enter author notices or
reviewer context. Each held comment appears in the home alerts card and as
a quiet notice in its PR room, with owner-only **Admit** and **Dismiss** actions.
The PR page shows only the held count. The owner can also run
`dyna git pr admit <pr> <kind:id>` or `dyna git pr dismiss <pr> <kind:id>`.
Admission covers only the exact held version; an edit is held again.

Non-owner comments are quoted as data, never instructions. Trusted comments
still pass the noise filter, which drops exact known rate-limit messages,
progress placeholders, and duplicate bot issue/review comments. Repeated
inline text can refer to different code locations, so it is kept.

The batch `Triage` interface cannot override a hold, an owner keep, or a
required rule drop. Kept comments for a recipient share one room post.
Edits are detected by content and update time. Reactions are not polled.
See [PR comment trust](pr-comment-trust.md) for configuration and controls.

## Teach the filter

See `dyna guide git`.

The noise command reads the original comment with `gh`. It refuses human comments
and unmarked owner comments. Inline comments, marked agent feedback, code blocks,
code locations, confidence reports and clear review findings stay protected.
An empty or very short template is also refused.

A learned rule stores the bot login and a normalized text template, scoped
to the repository and GitHub host. Normalization removes markup, numbers,
dates, user handles and links, while keeping the visible words. Comments with
review outcome labels keep their numbers, so a zero-issue template cannot
hide a changed count in prose, JSON, a table or HTML. Matching
requires equality of the whole normalized template: 100% similarity. Changed
wording or appended findings stay kept. A nonzero review issue count, including
label-first and structured counts, or a link to a review thread also prevents
a drop. The same template can match a
later PR in that repository; another bot or repository cannot use it.

`dyna git pr show <pr>` includes `noiseRules`, with each rule's ID, template,
reason, source comment, learner, creation time and match counts. `noise list`
shows all learned rules. `noise rm` removes a wrong rule immediately. Repeating
the same learn command returns the existing rule. Each repository can hold
up to 256 learned templates.

In the desktop and web app, open the room menu's **Pull requests** dialog to
inspect and remove learned rules. The list includes the room repository and
repositories of PRs tracked in that room, including ended PRs. Repository
scope includes the GitHub host. Rules are shared by rooms for that repository,
so removal applies to those rooms immediately. Removal never accepts a rule
ID from an unrelated repository.

The shared PR card's comment-filter count uses durable `commentDecisions`,
one latest decision per comment or review ID. It includes built-in and
learned drops and does not reset after a notice is delivered. An edited
comment that is later kept leaves the count. Rule removal does not change
past decisions. This differs from each wake's pending drop totals, which
can include several versions of one comment and reset after delivery.

Every wake post reports how many comments were dropped and names the rules.
Noise-only polls do not wake anyone. Their counts carry forward to the next
wake, so quiet filtering remains visible. Counts and kept comments appear
before the collapsible log tail.

## Registered CI machines

See [Repository-defined local CI](local-ci.md) for the suite file, trust rules,
cache declarations, and host-check permission.

Dyna can run CI on this machine or on registered SSH machines. These machines
are separate from GitHub runners and come from the shared machine registry
(`dyna machine list`). Local machines run merge trials only: GitHub's hosted CI
checks a pull request head, so a PR head push starts no local run. The engine
cancels work for superseded trial heads and never runs fork PRs on these
machines. `dyna git ci run <sha>` still checks one commit by hand, and the
change it belongs to reads that result at its next tick.

Slots limit concurrent checks across all runs on a machine. Each scheduled run
reserves up to six slots, so other heads can start while capacity is free.
Merge trials take free slots first; they do not interrupt active runs. On a
machine with 16 or more slots, three heads at six slots each finish in about
two minutes.

The local machine is the machine that runs this engine. It is the only row
marked `*` in the LOCAL column, and `--ci-slots` gives it the ci role:

```sh
dyna machine list
dyna machine add omarchy --ssh ari@omarchy --ci-slots 2
dyna machine edit this-machine --ci-slots 1
dyna git ci mode local
dyna machine edit omarchy --disable
dyna machine rm omarchy
```

`--disable` stops new CI work on a machine: its slots are not scheduled. The
flag belongs to the whole machine, so on the local row it covers that machine's
work, not the engine, which keeps running. `dyna machine add` needs a new name;
use `dyna machine edit` to change a stored machine.

### Rooms of a machine

The registry is shared by every room, so a machine can name the rooms that may
use it, and each room's CI then stays on the machines it owns:

```sh
dyna machine edit hermeslab --rooms dyna
dyna machine edit arch --rooms fl-rea,pve
dyna machine edit hermeslab --rooms ""
```

Dyna stores the room IDs, so a renamed room keeps its machine. An empty list is
the default and lets every room use the machine. Local CI scheduling and merge
trials skip a machine that does not allow their room, so that room's work waits
for a machine that does instead of running next to another room's work.
`dyna machine show <name>` prints the list, and taking the ci role away clears
it.

### Suites of a machine

A machine can also name the suites it takes, so a merge trial runs where its
suites can run:

```sh
dyna machine edit hermeslab --suites go
dyna machine edit arch --suites go,frontend
dyna machine edit hermeslab --suites ""
```

The suite IDs come from the `trial` list in
[the repository definition](local-ci.md), such as `go`, `cross-build` or
`computer-smoke`. An empty list is the default and lets the machine take every
suite. Merge trial scheduling sends a run only to a machine that allows every
suite of that run, and an integration sync follows the same rule. When no
enabled machine of the run's room allows a suite of it, the trial fails at once
with an error that names the suite, so a trial never waits for a machine that
cannot take it. A machine that is only busy or low on disk still counts, so its
room's work waits for a slot rather than failing. `dyna machine show <name>`
prints the list, and taking the ci role away clears it.

## Probing machines

Dyna reads what a machine has, and whether it is reachable, with one bounded
probe. A remote probe runs a fixed POSIX shell script over SSH with
`BatchMode=yes`, and the whole probe has a 20-second limit. The local row runs
the same script without SSH. No credential, token or setting goes with it.

The script prints `key=value` lines: OS and architecture, CPU count, MemTotal
and MemAvailable, the load averages, CPU use over one second, whether `/dev/dri`
exists, whether docker or podman exists, whether the computer dependencies
exist, and the remote `dyna --version`. The computer dependencies that
registration checks are labwc and Xwayland; see
[Agent computers](computers.md). A machine is shown with computers only when it
has both. Computers are a capability, not a role. wayvnc only streams a desktop
to the app, so the probe reports it as a separate `stream` capability.

```sh
dyna machine probe hermeslab
dyna machine probe --all
```

`dyna machine list` has a STATUS column: `online`, `offline`, `disabled` or
`unknown`. `dyna machine show` prints the capabilities, the load, the time the
probe last reached the machine, and the error of the last failed probe.

The engine probes every enabled machine when it starts, when a machine row
changes, and every five minutes. It runs at most four probes at once. A failed
probe keeps what the last good probe read, and marks the machine offline with
the error. A disabled machine is not probed, and shows `disabled`.

The mode applies to the current room. A pull request head is always checked by
GitHub's hosted CI: no mode starts a local run for it. The default, `github`,
reads only hosted evidence. `local` and `both` use a saved local result for the
head when one exists, such as the result of `dyna git ci run`, and fall back to
hosted CI otherwise. A conclusive pass permits progress when every required
check has passed. An observed failure from either source blocks it. Review
approval remains required. A merge trial runs on the local machines that can
take its suites, whatever the mode.

When a machine starts a merge trial, Dyna cancels that trial head's queued or
waiting runs of `.github/workflows/ci.yml`. Running jobs and other workflows
finish. An infrastructure error makes Dyna rerun those cancelled runs. A test
failure remains a failure. Dyna never cancels a pull request head, a main push
or a fork PR run. The GitHub workflow stays unchanged and always checks landed
code on main.

Each machine limits concurrent checks with its slot count. Local work runs with
systemd CPU and memory limits and a lower process priority. Containers also
limit CPU, memory, and process counts. Machines reuse repository-declared
caches under the runner's separate `caches/` directory. Every cache has a size
cap and age cleanup. A run that finds a linked
go-build or go-mod path keeps the link beside the cache as `.before-d48`,
creates a real directory under the exclusive cache lock, and posts a typed
machine notice. Any other unsafe cache link stops the run, so it never mounts
or trims another account's cache.
Results and machine settings live in SQLite; report files remain on disk.
Dyna keeps a local run's result to itself: it posts no commit status to GitHub,
so no machine name leaves the engine. The merge gate, the PR Checks tab and
`dyna git merge status` read the saved result, which names the machine that ran
each check. Use `dyna git ci show <sha> --json` for saved results and durations.
A status Dyna posted for an older run stays on that commit; the watcher ignores
it, so it never counts as hosted evidence for a merge.

## Host watcher and stored state

The engine runs PR watching, goal keeping, and question delivery in one worker
loop. PR input changes trigger a tick. A one-minute fallback checks due PRs,
including quiet PRs. The engine also ticks at startup to catch stored writes
made while it was down. CLI commands write the stores directly and start no
background process. Run `dyna engine run` through the installed service.

The PR store lives in `dyna.db`: one `pr_rows` row per PR and one
`pr_state` row for shared state. A PR row holds the fields that change on every
poll. Its notices, review history and detail, and cached responses live in
`pr_record_parts` rows (`notices`, `detail`, `responses`), so a poll writes
only the small row. A change writes only its changed rows and parts. Older
dyna versions name the table `pr_records`; the rename makes them fail instead
of reading a partial record.
The first open imports an old JSON store (`index.json`, `records/`, `closed/`
or `state.json`) and leaves those files unchanged.
Closed and merged records expire 14 days after closure; active records remain.
The state also stores request validators and cached responses, per-PR due
times, shared rate-limit state, repo-scoped learned noise templates, and pending
drop counts. Cached responses contain GitHub data and use the store's private
file permissions. Read-only session link queries use `internal/pr/links` to avoid a
dependency cycle between the watcher and session responsibility checks.
The room service connects session close to the normal PR transfer path.
Closing transfers author roles to the nearest open ancestor, or root, with
author history and pending notices intact. Reviewer roles return to the pool.
A failed handoff keeps the session open unless force close is chosen.
A held goal that would reach root is resolved first: root asks the owner with
`dyna request question` when only the owner can unblock it, then finishes or drops the goal,
or hands it to an open agent. A forced close never leaves the goal with the owner.
The engine service owns the single worker process and its restart policy.
Worker errors go to the engine log. The database serializes writes.

The outbox assigns a stable room post ID to every batch. A retry reuses the
same post, including after a lost delivery acknowledgement. Logs and comment
batches are capped. Known token formats and secret environment values are
redacted. Authentication stderr from `gh` is not copied into room posts.
Tests use fake `gh`, a fake clock, and temporary stores without network calls.

## App API

Desktop, paired web clients, and Connect clients share these calls:

| Method | Result |
| --- | --- |
| `TrackedPRs(roomRef)` | PR rows, newest first; an empty room ref lists all rooms. |
| `PRSettings(roomRef)` | `{leadSession, reviewerProfile, reviewerSession, reviewPublishing}`; empty strings mean off. |
| `SetPRSettings(roomRef, settings)` | Validates and atomically saves the whole setting, returning canonical session IDs. |
| `UntrackPR(roomRef, key)` | Stops watching that room's PR. |
| `ResumePR(roomRef, key)` | Resumes watching and clears the CI pause. |
| `CloseSession(roomRef, sessionRef)` | Closes after automatic handoff; returns `closeSummary` lines. |
| `ForceCloseSession(roomRef, sessionRef)` | Closes despite a failed handoff; reports unresolved work. |

Rows include `key`, `repo`, `number`, `url`, `title`, `roomId`, `authorSession`,
`reviewerSession`, `reviewerMode`, `reviewPublishing`, `reviewerName`, `leadSession`, `state`,
`watching`, `status`, `ci`, `checks`, `consecutiveFailures`, `ciPaused`,
`pauseReason`, `error`, `reviewRound`, `reviewHead`, `verdict`, `summary`,
`reviewUrl`, `head`, `created`, and `checked`. Before assignment,
`reviewerSession` shows the configured named reviewer when one exists.
Each check has `name`, `state`, `status`, `conclusion`, and `url`. When `url`
is empty, `commentUrl` links to the same bot's latest posted comment or review,
when known. Clients use `url`, then `commentUrl`, then the PR URL. Pending
reviews and human comments cannot supply this fallback. Its normalized
state is `pending`, `success`, `failure`, `skipped`, or `cancelled`. Last known
check details remain available after merge or close.

The derived row status is `ci-running`, `ci-failed`, `awaiting-review`,
`changes-requested`, `approved`, `paused`, `merged`, `closed`, or `untracked`.
The clients receive `{kind: "prs", roomId}` through the existing SSE
event stream when records or settings change, including changes from another
process. Reads do not create or change PR records.

Watcher feed entries use `kind: "pr"`, exposed on `roomsvc.Post`. Their text
starts with the PR URL, a `Head: <sha>` line, a blank line, then the batch text.
Log tails start with a blank line and `Log tail:`. Clients can use a compact
system row and fold the log tail while preserving the full stored batch.
