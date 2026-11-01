# Migration gate design

This gate helps every owner moving from v1. It checks a frozen v1 backup before
the irreversible cutover. This is a counts-only rehearsal, not the M3 store
import or the owner-approved cutover gate.

## Boundary

Main has no v1 tree (owner, 2026-11-01). `cmd/dyna-export` and the v1
`internal/` packages it reads exist only at annotated tag `v1-final`
(`4f2192eb24`). Build the exporter from that tag, with Go 1.26.10:

```sh
git clone --branch v1-final https://github.com/resident-labs/dyna
cd dyna/cmd/dyna-export
go build -o dyna-export .
```

The README beside it lists the flags. At the tag, the exporter is its own
module, `dyna-agent/cmd/dyna-export`. It replaces `dyna-agent` with `../..` and
`github.com/resident-labs/dyna` with `../../next`, so it imports the
standard-library-only `next/contract` of the tag without copying its types.
No CI builds it any more. This module layout supersedes spec 8.2's old-module
replacement (lead decision, 2026-10-31).

The exporter is frozen at the tag, but `next/contract` and the importer are not.
It writes `next/contract` as of `4f2192eb24`, and `DecodeBundle` rejects unknown
fields. A bundle type in main can therefore drift away from what the exporter
writes, and nothing tests that. Any change to `next/contract/bundle.go` or to a
type it names must still accept that output: new fields stay optional, existing
fields and enum values keep their JSON. A change that cannot do that must bump
`BundleV1` with a conversion, or pair with a fixed exporter on a new tag cut
from `v1-final`. `v1-final` itself never moves.

The exporter opens an explicitly supplied backup with SQLite read-only, never
calls v1 store open functions that migrate, and fails on unsupported schema.
It also needs a consistent, private copy of the non-database inputs below:
a database backup alone cannot reproduce settings, profiles, skills, feed
bodies, hook scripts, or Git refs. These files must be captured with the final
backup after the pause. Never read credentials. The output directory is 0700
and its files 0600. The archive is outside this counts-only rehearsal.

## Source map and selection

| Bundle part | v1 source | Selection / conversion |
|---|---|---|
| Standing instructions | `goal_rooms.body`, `internal/goals/sqlite.go` | Render global room `*` and each room's rules as separate documents; verify each text and its order against SQL. |
| Settings | `settings.json`, `internal/settings/settings.go` | only contract fields; minutes to milliseconds |
| Profiles | `profiles.json`, `internal/profile/profile.go` | proxy-supported models; mapping needs review below |
| Hosts | `machines`, `internal/machines/store.go` | enabled remote hosts only; room IDs resolved to names |
| Accounts | `accounts.metadata`, `account_repo_scopes`, `internal/account/store` | provider, label, repository scope; never credentials |
| Devices | `web_devices` and scope grants, `internal/websvc/devicestore`, `internal/devicesvc` | token hash, name, dates, scopes; never tokens |
| Repos and room metadata | room records, repository settings, `internal/room/room.go` | resolve repository paths to repository identities; preserve default |
| Skills | skill registry and `SKILL.md` files | installed package reference or owner body |
| Subscriptions | hook documents, `internal/hooks`, script files | only hooks that map cleanly to Wake; report others and include them in the handoff |
| Goals and items | `goals`, `goal_items`, `internal/goals/sqlite.go` | open goals and all their items; no holder |
| Issues and next number | `room_issues`, `room_issue_numbers`, room JSONL feed; `internal/room/issues.go` | open and goal-linked; retain number, body and notes |
| PRs | `pr_records` and record parts, `internal/pr/state` | open tracked PRs; branch and ownership only |
| Questions | `questions`, `internal/questions` | unanswered and not withdrawn; include pending terminal requests, mapped to show_pty |
| Handoff | session store, `internal/subagent/store.go`, room feed and Git refs | open sessions only; resolve parent and goal labels; last post capped at 2,000 characters |

The contract does not count profiles, repositories or skills. Their lengths
should still appear in the report, separately from `BundleCounts`.

## Approved mapping decisions and remaining inventory

The lead approved the following additive bundle fields and mapping decisions. Do not silently discard or invent data.

* `GlobalBundle.Rooms` is the room manifest. The importer rejects missing,
  extra, duplicate or name-mismatched room files.
* A v1 terminal request maps to `show_pty`. It has `Argv`, `Cwd`, `Reason`, `Stage` and `Holder`,
  and `Command` alone loses argv boundaries and cwd. The bundle carries argv,
  cwd and reason; unanswered requests go to root.
* Hook `Command`, `Cwd`, `Timeout`, `OnFail` and script dependencies do
  not directly supply `Run.Image`, `Run.Size` and `Run.Files`. Do not invent
  a Run mapping. Report unmapped active hooks and include them in the handoff.
* Hosts have `Enabled` and `Local`, neither in the bundle. Profiles have
  `Disabled`, `Harness` and `Uses`, and provider model IDs may differ.
  Skip disabled profiles; use an explicit model map and report unknown models.
  Account rooms have a bundle field. Map known device scopes and report unknowns.
* Rule `SetterName` and `Edits`; goal `Holders`, `Shared`, `NextItem`,
  `ItemsClosed`, `State`, `UpdatedAt`, `Edits`, `Done`,
  `ExpediteUntil`, `LegacyOwnerHeld`; item `UpdatedAt`; question `To`,
  `Post`, `State`, `Answer`, `Withdrawn`, `GroupKey`, `AskedTurn`
  and `AnswerDelivered` have no direct bundle home. Many describe dropped
  history or live v1 execution, which spec 8.1 drops. Machine probe, resource,
  grant and scheduling fields are dropped because v2 probes hosts again.
* Source `Commit`, `Backup`, `BackupSHA256` and export time are generated,
  not record fields. The exporter report must classify every v1 field as carried,
  dropped by spec 8.1, or a gap. Raise each gap for the lead to decide.

## Independent check and dry run

`dyna-export verify` reads the same immutable input copy and bundle. It runs
separate SQL count queries, not exporter selection or count functions, per
room and global. It compares every count with independently decoded bundle
lengths and declared counts, including zero categories and item joins to open
goals. File-backed source counts must be independently counted from the
captured files; SQL cannot count records absent from SQLite. A count match
alone cannot prove field preservation or detect equal-count substitutions.

`dyna import --dry-run <bundle>` opens no store and writes nothing. It reads
the global part and each manifest-named room part; `DecodeBundle` decodes
all records using contract types, refusing unknown fields, trailing data and
unknown versions. It rejects absent parts and mismatched per-room and global
counts, then reports only counts. Check path-safe room names before opening
files. A report compares SQL or file source, bundle and dry-run counts.

Tests use invented tiny inputs: one fail-first case each for unknown version,
missing part, unknown nested field and wrong count. Each runs under one second
without sleeps. Also test independent SQL filters and a successful multi-room
bundle. Run checks on a build host under r19.

## Estimates

Product lines, excluding tests and third party: `cmd/dyna-export` about
900–1,300; `next/migrate` about 200–300; `next/cmd/dyna` about 30–50;
`next/contract` about 5–15 for the manifest. Estimates exclude any policy
or contract expansion above. Exporter and verifier share the first package
but not selection/count code. The exporter PR used the v1 merge queue, which is
gone from main with the rest of v1; the dry-run PR touches only `next/`.


## Rehearsal: 2026-10-31

The exporter, verifier and dry-run importer passed on a copy of the latest
daily backup. The backup preceded the file capture by about 22 hours and
33 minutes. The live feeds were read later. This non-atomic rehearsal is not
a cutover gate. The captured backup, bundle and other private files were
deleted after the run.

| Category | Checked source | Bundle | Dry-run |
|---|---:|---:|---:|
| Rules | 52 | 52 | 52 |
| Goals | 20 | 20 | 20 |
| Goal items | 326 | 326 | 326 |
| Issues | 412 | 412 | 412 |
| PRs | 51 | 51 | 51 |
| Questions | 0 | 0 | 0 |
| Subscriptions | not independently checked | 0 | 0 |
| Hosts | 4 | 4 | 4 |
| Accounts | 10 | 10 | 10 |
| Devices | 2 | 2 | 2 |
| Sessions | 36 | 36 | 36 |
| Profiles | 0 | 0 | 0 |
| Repositories | 5 | 5 | 5 |
| Skills | 1 | 1 | 1 |

Database source counts use independent SQL. Repository counts also use the
captured identity manifest. Profile and skill counts use captured config and
the sanitized skill catalog. The verifier at this revision did not
independently count subscriptions. Equal counts do not prove field equality
or captured-file completeness.

Six disabled profiles were skipped. Nine enabled profiles had no known model
mapping, so no profile was exported. This blocks cutover until the model table
is checked. The report also counted two skipped hosts, one skipped account,
five unsupported hooks, twelve repository gaps, four skipped PRs, ten skipped
services, and 27 payload-free legacy issue events. It found no partial feed.
The final export and dry run had no decode errors. Earlier attempts found a
missing legacy issue payload and the absent `pr_records` table; the exporter
now handles legacy events and reads `pr_rows`.

### Field decisions

The lead classified all Session and Settings fields in the inventory as
dropped by design. TrackedPR fields other than Hold are dropped. Profile
RuntimeVersion, Output, ExtraArgs, Env, TimeoutSec, Managed and SafeMode,
and Question Human are dropped. Env is never carried: it may hold secrets.

Room MergeMaxParallel, GitHubAccess, ApprovalWait, ApprovalExpiry,
LibraryPinSet, LibraryPin, CheckIn, DisplayName, ManualArchive, Pinned,
PinOrder, CreatedBy, CreatedAt and LastActivity are dropped. Attribution,
AttributionPlacement and AttributionKinds follow the agreed attribution
mapping and gap report. Rule SetterName maps to the author when possible;
otherwise it is dropped.

A PR Hold and room RootProfile, SessionPause and RepoUpstreams are handoff
data. A room pause is carried as the room pause. Hooks that do not map
cleanly are named with their blocking fields in the handoff or private report.
The exporter report must identify any remaining gap for a lead decision.


### Approved profile mapping

| v1 profile | v1 model | Proxy model |
|---|---|---|
| glm | `zai/glm-5.3` | `glm-5.3` |
| glm-flash | `zai/glm-5.3-flash` | `glm-5.3-flash` |
| haiku | `haiku` | `claude-haiku-5-5` |
| luna | `openai-codex/gpt-6-luna` | `gpt-6-luna` |
| opus | `opus` | `claude-opus-5-5` |
| opus-rewrite | `opus` | `claude-opus-5-5` |
| sonnet | `sonnet` | `claude-sonnet-5-5` |
| terra | `openai-codex/gpt-6.1-terra` | `gpt-6.1-terra` |
| wonder-full | `meta/muse-wonder-full-1.0` | dropped: Muse is unsupported |

The exporter carries the thinking level, including `xhigh` for opus-rewrite.
The real import maps `ProfileBundle` fields `description`, `taste`, `diagnostics`, `intelligence` and `cost` to the matching atomic profile config fields.
The real import must check each profile model ID against the supervised proxy's live model list, without reading its config, and refuse each missing model by name. The offline dry run lists mapped IDs as checked at import.
The six skipped profiles (deepseek, fable, grok, kimi-max, mimo and sol)
were disabled in the captured catalog. The unsupported Muse profile is a
report and handoff line, not a cutover blocker.

Subscription source count is zero by policy: no v1 shell hook maps to a typed
subscription. The five unsupported hooks remain handoff records. The verifier
uses an empty SQL eligibility set; it does not independently classify hooks.

# Updated per-room rehearsal

Export, independent verification and offline dry-run passed. This non-atomic rehearsal is not a cutover gate. The backup was last modified 2026-10-30 18:38:35 UTC. Configuration was captured 2026-10-31 17:28:34 UTC, about 22 hours 50 minutes later. Feeds were read in place later. Repository default rooms are provisional.

Each cell shows SQL source / decoded bundle / dry-run. The verifier compares the SQL and bundle counts separately for each room. Subscription source is 0 by policy: no v1 shell hook maps to a typed subscription. The SQL uses an empty eligibility set and does not classify hooks independently.

| Scope | Category | SQL / bundle / dry-run |
|---|---|---|
| global | rules | 4 / 4 / 4 |
| global | hosts | 4 / 4 / 4 |
| global | accounts | 10 / 10 / 10 |
| global | devices | 2 / 2 / 2 |
| dyna | rules | 15 / 15 / 15 |
| dyna | goals | 5 / 5 / 5 |
| dyna | goalItems | 2 / 2 / 2 |
| dyna | issues | 24 / 24 / 24 |
| dyna | prs | 2 / 2 / 2 |
| dyna | sessions | 8 / 8 / 8 |
| fl-rea | rules | 14 / 14 / 14 |
| fl-rea | goals | 10 / 10 / 10 |
| fl-rea | goalItems | 283 / 283 / 283 |
| fl-rea | issues | 373 / 373 / 373 |
| fl-rea | prs | 25 / 25 / 25 |
| fl-rea | sessions | 18 / 18 / 18 |
| horizon | rules | 11 / 11 / 11 |
| horizon | goals | 2 / 2 / 2 |
| horizon | issues | 14 / 14 / 14 |
| horizon | prs | 24 / 24 / 24 |
| horizon | sessions | 2 / 2 / 2 |
| cozycraft | rules | 8 / 8 / 8 |
| cozycraft | goals | 3 / 3 / 3 |
| cozycraft | goalItems | 41 / 41 / 41 |
| cozycraft | issues | 1 / 1 / 1 |
| cozycraft | sessions | 8 / 8 / 8 |

| Category | Checked source / bundle / dry-run |
|---|---|
| profiles | 8 / 8 / 8 |
| repositories | 5 / 5 / 5 |
| skills | 1 / 1 / 1 |

Zero counts are omitted from the per-room table. The verifier checked them. Repository source counts use SQLite and the captured identity manifest. Profile and skill counts use captured files. Equal counts do not prove field equality or captured-file completeness.


The updated run exported eight profiles and recorded no unknown model mappings. Six disabled profiles were skipped. The Muse profile is reported as unsupported. The field classifications above apply to this run.

## Standing instruction documents

The exporter renders each scope's v1 rules in ID order under `# Instructions`. Each rule is one plain Markdown list item. The first line starts with `- `; each later line, including blank lines and code fences, has two leading spaces. Empty scopes produce empty documents. The verifier parses each document and independently compares its texts against v1 SQL in order. Rule IDs, authors and timestamps are dropped.

The bundle carries the rendered documents, not rule records. After the store commit, the real import writes each file unchanged by rename to `<data>/files/instructions/global/AGENTS.md` and `<data>/files/instructions/rooms/<room-id>/AGENTS.md`. The room ID is the new ID created by the import. The offline dry run writes neither file.

