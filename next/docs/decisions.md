# Decision history

Decisions made since the v2 rewrite, oldest first. The current design and the settled owner decisions are in [spec.md](spec.md); this file records why.

## The v2 rewrite

The v2 rewrite ran from 2026-10-30 to the cutover, under a rewrite lead with one owner per area, milestones M0 to M3 with hand checks in a sandbox, line budgets per package, and design panels of mockups, judges and T3 Code side-by-sides. That process ended with the rewrite: the dyna room's standing rules now set how work is built, reviewed and merged, and spec 5.13 sets how a view change is reviewed.

The full history, with sources, rationale, accepted risks, milestone evidence and M0's provider limitations, is the unabridged pre-split spec at commit `738c66c6ac620d7e8aca4d78f8498ff4276d0de6`, and this file as it stood at `22acedaf03411cbb8b2a4e837f37baee26367db7`, before this summary.

## Declared agent surface (owner q68)

The owner chose to ship without the five-provider evaluation gate. A Claude sanity run passed both shapes. Declared used 165,534 tokens against 179,099 for caller; this was not a controlled comparison. The lead chose declared for its simpler model and lower token count in that run. Caller ended one turn with text about the wrong action. The old surface and switch are removed.

## Host skills and exec-only hosts (owner, 2026-11-03)

- **Host skills.** `dyna skill save HOST:PATH` copies a skill from a registered host over SSH, owner only, and records the host with the skill. The box agent says on the skill's first lines where its commands run, so agents use the existing `bash` `host` option; there is no new permission. A local folder with a colon needs `./`.
- **Run-on hosts (owner, 2026-11-03).** Dyna owns a saved skill; `HOST:PATH` is only a way to copy files in. Where its commands run is `hosts` on the stored skill, set with `--host` (repeatable) on save or with `dyna skill set` (`addon.set_hosts`). A re-save keeps them unless `--host` is given. Old single `host` entries read as `[host]`. Every host-exec run imports the desktop session variables the user's manager lists, on full hosts too, so no skill-specific rule is needed.
- **Exec-only hosts** are an explicit stored flag set by `host add --agents off --ci off`, not derived from the two switches, so an old host with both off stays a disabled full host. They need SSH and bash only, with no admin flag.
- **8 MiB cap.** A run's output and the files it leaves in `$DYNA_OUT` are each capped at 8 MiB (`hostexec.OutputLimit`). Over the cap, the output is cut and the files are not copied back, and the result says so.

## Root-approves switches per room (owner, 2026-11-03)

- **Four kinds, one set.** A room has a set of `RootApproval` kinds (`hostexec`, `addons`, `hosts`, `pools`) in `rooms.root_approves`. Each kind that is on lets the room's root decide that kind's owner-class operation (`hostexec.grant`, `addon.global`, `room.hosts`, `pool.add`) with `op decide`. The owner still can. Each is off by default, and off is the behaviour before the setting existed.
- **Owner only to change.** `room.set` is the owner's, and it refuses an unknown kind. Settings > Approvals and `dyna room set --root-approves-KIND on|off` reach it. An Autopilot button turns all four on, or off.
- **Left out.** CI switches (`repo.ci.set`, `host set --ci`) are direct owner-only calls with no request to decide, and host CI is not per room. Merging approved PRs is not an operation Dyna has (spec 7.6). Neither has a switch.
- **The root may decide its own request.** When a kind is on, a root's own request in its room waits for a decision like any other, and the root may make it. That already held for `hostexec` and spec 3.5 states it; the owner has the question of whether it should stay.

## Root sees its room's switches in `dyna_room` (owner gap via root, 2026-11-03)

- **Gap.** `room.set` changed `rooms.root_approves` and told nobody, so a root could not know it might now decide those requests.
- **A live section, no notice.** A new context section `dyna_room` is for the room's root session only (the typed `Session.Root`), one line per non-default room setting that changes what root may do. Today: `Approvals you decide: host exec, global add-ons`, the kinds that are on, in Settings order, named as Settings names them. It describes and does not prescribe. With every switch off it is not rendered. A flip reaches the root as a changed-section diff at its next turn, like `dyna_children`, and wakes nobody; turning the last switch off removes the section. A later room setting joins by adding one line in the renderer.

## A live activity has a typed kind (review of the thread's compaction line, 2026-11-03)

- **Decision.** `turn.activity`, its request and `SessionSummary` carry a closed `ActivityKind`, `tool`, `compaction` or `model`, beside the label. The harness sets it (Pi's tool start, compaction start and model transfer); the label only words it. The web decides from the kind and never compares label text, so the thread's "Compacting the conversation…" line follows the same live activity as the sidebar.
- **Old peers.** The field is additive. A report with no kind, from a box agent that predates kinds, is stored as `tool`; an unknown kind is refused at the engine, which owns the set. The web reads a summary or event with no kind as plain work.

## Profile list keeps read-only fields apart (build-lead via root, 2026-11-03)

- **Wrapper, not a nested status.** `profile.list` returns entries `{profile, inUse}`. `profile` is the whole stored `contract.Profile`, so it is a valid `profile.set` body; `inUse` is beside it. A nested `status` inside the profile would have to be accepted and ignored by `profile.set`, which loosens the strict decode. `ProfileSummary` is gone; the web builds its row type (`Profile` plus `inUse`) once in `web/src/lib/profiles.ts`.
- **`images` is always explicit in the list**, so a set built from it cannot drop `images: false` or turn it on by omission.
- **`set` merges in the CLI.** `profile.set` stays a whole replacement; `dyna profile set` loads the profile first and sends it back with the given flags applied. The read and write are two calls, so an edit between them is overwritten, which for one owner at a CLI is accepted.
- **Unknown field is named at the API boundary**: `unknown field "inUse"`, for every method, as the decoder words it. The list of fields the method takes is not added, because a nested field has no short list.

## Boxes send no Pi documentation block (owner via root, 2026-11-03)

- **Decision.** The system prompt of a Dyna box has no `<docs>` section, about 330 tokens that point at Pi's own documentation and tell the agent to read pi `.md` files. The box agent passes `--no-docs`, a Pi fork flag (P14, fork level 15) that leaves out that one section. Nothing else in the prompt changes.
- **Why a fork flag.** Pi v1.4.0 has no option for it: `--system-prompt` replaces the tools and rules too, and `docs` is a reserved section name an extension cannot remove. The flag is the one place that changes; the box agent has no prompt text of its own to keep in step.
- **Where the docs are.** They stay at `/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/docs`, and the `dyna-extend` skill, the one place an agent learns about extending Dyna, names that path.

## Settings list pages share one list and detail (owner, 2026-11-03)

- **Why.** At 100 rooms, Approvals drew 100 cards, 400 switches and 400 copies of the same four notes; Profiles drew 1000 table rows at once, and no list could be searched. The owner approved the Settings-at-scale mock and asked for it built, one page per PR, Approvals first.
- **One pattern, built once.** `ui/settings` holds the search box, the windowed item list (chips with counts, optional sortable columns, folding groups, the loading, failed, empty and no-results states, the keys) and the list and detail layout. Its logic (search, chips, sort, groups, the window and the keys) is plain TypeScript in `settings-list.ts`, tested with node. Pages pass typed items, chips, columns and groups; nothing in the shared code reads a label.
- **Fixed-height windowing of its own.** The rows have two fixed heights (an item and a group head), so the window is a binary search over row tops, and the keys move by row and by page from the same tops. `VirtualList` measures rows of any height for the feed and threads and stays there.
- **Help text once.** A sentence that explains a setting sits in the item's detail; rows show values. The empty state names what is missing and does not explain the feature.
- **In the browser, whole lists.** `room.list` and the other list calls return everything, and the page filters a few thousand items in milliseconds. Server-side paging (query, filter, sort, limit, a cursor, counts per chip) waits until a list outgrows that.
- **Approvals (owner's defaults).** No switch for every room in a filter; the four switches and Autopilot stay per room, Autopilot with its confirm. The open room is in the path, so a phone's Back returns to the list.
