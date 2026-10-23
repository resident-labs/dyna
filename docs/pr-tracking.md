# PR tracking and agent review

Dyna links a GitHub pull request to its author session. A host process uses
`gh` to check watched PRs every minute, including quiet PRs.
Sign in with `gh auth login` first.
Tracking is explicit because a shared branch does not identify which agent
opened a PR. The engine merges approved PRs with passing CI through batch
trials (see `dyna guide git`); agents never run merge commands.

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
earlier approvals stop counting. `dyna git merge status` and the PR page show
the missing reviewers. Releasing a draft does not bypass this review gate.

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

## Registered CI nodes

Dyna can run CI on this machine or registered SSH machines. Nodes are separate
from GitHub runners. The engine gives merge trials priority, then takes PR
heads in oldest-first order. It cancels work for superseded heads and never
runs fork PRs on these machines.

Slots limit concurrent checks across all runs on a node. Each scheduled run
reserves up to six slots, so other heads can start while capacity is free.
Merge trials take free slots first; they do not interrupt active runs. On a
node with 16 or more slots, three heads at six slots each finish in about two
minutes.

```sh
dyna git ci node add local --slots 1
dyna git ci node add omarchy --ssh ari@omarchy --slots 2
dyna git ci node list
dyna git ci mode local
dyna git ci node disable omarchy
dyna git ci node rm omarchy
```

The mode applies to the current room. The default, `github`, keeps hosted CI.
`local` and `both` let local and GitHub results race. A conclusive pass permits
progress when every required check has passed. An observed failure from either
source blocks it. No enabled
nodes means GitHub remains the source. Review approval remains required.

When a node starts a PR head or merge trial, Dyna cancels that head's queued
or waiting runs of `.github/workflows/ci.yml`. Running jobs and other workflows
finish. An infrastructure error makes Dyna rerun those cancelled runs. A test
failure remains a failure. Dyna never cancels main pushes or fork PR runs.
The GitHub workflow stays unchanged and always checks landed code on main.

Each node limits concurrent checks with its slot count. Local work runs with
systemd CPU and memory limits and a lower process priority. Containers also
limit CPU, memory, and process counts. Nodes reuse Go and package caches.
Results and node settings live in SQLite; report files remain on disk.
Dyna publishes named commit statuses, including `go-test`, using the room's
GitHub credentials. The PR Checks tab and `dyna git merge status` show nodes.
Use `dyna git ci show <sha> --json` for saved results and durations.

## Host watcher and stored state

The engine runs PR watching, goal keeping, and question delivery in one worker
loop. PR input changes trigger a tick. A one-minute fallback checks due PRs,
including quiet PRs. The engine also ticks at startup to catch stored writes
made while it was down. CLI commands write the stores directly and start no
background process. Run `dyna engine run` through the installed service.

The PR store lives in `dyna.db`: one `pr_records` row per PR and one
`pr_state` row for shared state. A change writes only its changed PR rows.
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
