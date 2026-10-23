## Problem

The PR watcher posted free text and `web/src/lib/prs/pr.ts` guessed what it meant with a regex table (`CI failed 1/3`, `changes`, `approved`, `merged`, …). A wording change in the watcher silently broke the card, and nothing tied a stored post to the fact it reported. A session DM showed the same notices as long raw text, log lines included.

This PR replaces that with typed `pr/*` events and draws the PR card, the feed and a session DM from the payload.

## What it deletes

- `PRPost.svelte` (replaced by `PRCard.svelte`) and `room.PRNotice` (replaced by `room.EventNotice`, which holds the event type; the facts are in the typed payload).
- The chip, body and noise parsing in `feedRows.ts` for new posts (`feedPRDetail`, `feedPREvents`). The text reader in `pr.ts` stays as a small `// TODO: legacy/shim` reader for posts stored before this change.
- `Message.PR()` reads the payload; the watcher no longer builds a `room.PRNotice{number, head, changeId}` by hand.

## Typed `pr/*` events

`internal/room/events.go` adds `PREvent` and the `pr` family, sealed like `worktree` and `hook`: `Store.Post` rejects it, and only `PostEvent` writes it. Every field is typed; a required field is one whose json tag has no `omitempty`.

The payload carries the change (`label`, `number`, `changeId`, `repo`, `title`, `head`, `url`, `branch`), the fact (`type`, `ciAttempt`/`ciMax`, `summary`, `commenter`, `commentId`, `verdict`), and the agent body (`detail`, `logTail`, `dropped`). `Message.PR()` reads it and decodes strictly. The post ID is the notice's own stable `msg_` ID, so a retry posts once and a transfer keeps the recipient it had.

One-line agent text templates, from the registry. The event body (`detail`) and the CI log tail (`logTail`) follow the template as their own paragraphs; a block that only repeats the template adds nothing.

| Type | Template |
|---|---|
| `opened` | `{label} opened in {repo}. Review it at {url}` |
| `ready` | `{label} is ready to land at {head}.` |
| `approved` | `{label} at {head} approved by its Dyna reviewer.` |
| `changes` | `Reviewer requests changes for {label} at {head}. Fix the findings and push to this PR.` |
| `ci-failed` | `CI failed ({ciAttempt}/{ciMax}) on {label} at {head}: {summary}. Fix the failure and push to this PR.` |
| `ci-paused` | `{label} failed CI three times in a row. Later CI failures will not wake the author. Failed jobs: {summary}.` |
| `ci-passed` | `CI passed on {label} at {head}.` |
| `returned` | `{label} at {head} was returned to you. {summary}` |
| `merged` | `{label} merged.` |
| `closed` | `{label} closed.` |
| `comment` | `{label} at {head}: {summary}` |
| `held-comment` | `{label}: Held: comment from {commenter}, who is outside the trusted set. Review this comment in Dyna to admit or dismiss it.` |
| `review-assigned` | `Review {label} at {head}.` |
| `update` | `{label} at {head}.` |

`{label}` is the watcher's own `changeLabel`, such as `PR #542` or `Change <key>`, so a local change needs no PR number. The CI notice keeps its failing check names and its `Log tail:` block, so its length does not change.

## Routing and wake

| Type | Receiver | Wakes |
|---|---|---|
| `ci-failed`, `changes`, `returned` | author, else nearest open ancestor, else owner | the author |
| `approved`, `ready`, `merged`, `closed`, `opened`, `ci-passed`, `ci-paused`, `comment`, `held-comment`, `review-assigned` | same | the author (unchanged: an ended change wakes its author so it can finish its goal) |
| `update` (informational: budget notes, radar conflicts, draft changes, merge reports PR 3b will type) | same | nobody |
| any type with `needsRoot` | same | root |

A post that falls back to the owner is quiet unless `needsRoot`, as before. `TestPREventRoutingAndWake` is the table test over every row.

The sender of an event post is an engine sender, never a session: `dyna`, or the room's own service sender for a PR notice. That ends the case where a PR notice to the owner appeared to come from the author session.

## Web

- `PRCard.svelte` reads the typed payload. The feed passes the folded posts of the row and the room's post bar; a DM passes one post. Phone and desktop keep the card they have.
- `eventRenderers.ts` maps every `pr/*` type to `PRCard`, so the registry test fails if a type has no card.
- A session DM maps a delivered post with a registered event to a `system-event` row and draws the same card with `surface: 'dm'`. The agent text sits behind a shared `EventDetails` toggle, collapsed by default. This covers `pr`, `hook` and `worktree`, and later PRs get it for free.

## Tests

Go (`go test ./...`, `go vet ./...`): green.

- `internal/room`: the PR event stores its facts and text, a retry keeps one post, another comment on the same head is a second post, and an event without a label or change is rejected. `Store.Post` rejects the `pr` family.
- `internal/pr/watch`: `TestPREventRoutingAndWake` (receiver and wake per type), `TestPREventRetryKeepsOnePostPerNotice`.
- Web (`npm run check`, `npm test`, `node scripts/check-typescript.ts`): green, 1409 tests.
  - `prCard.test.ts` (fast): chips per type, the review chip a plain update keeps on the same head, the legacy reader.
  - `eventRenderers.test.ts`: every registered `pr/*` type renders a real card on the feed surface and on the DM surface.
  - `dmEvent.test.ts`: a CI-failed PR post reaches a DM as a `system-event` row, the card shows the facts, and the raw text is hidden until Details opens.

## Live QA

Pending: desktop (1280 px) and phone (390 px) screenshots of a CI-failed, an approved and a ready PR event, in the feed and in an agent DM with Details closed and open.

## Follow-ups

- PR 3b types the merge notices (`merge_*.go`) as `merge/*` with its own card. Until then they post as `pr/update` with their text in the body.
