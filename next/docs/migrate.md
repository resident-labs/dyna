# Migration gate design

This gate helps every owner moving from v1. It checks a frozen v1 backup before
the irreversible cutover. This is a counts-only rehearsal, not the M3 store
import or the owner-approved cutover gate.

## Boundary

The old module adds `cmd/dyna-export`, requires
`github.com/resident-labs/dyna`, and replaces it with `./next`.
The exporter imports the standard-library-only `next/contract`; it never
copies its types. Old CI compares `go list -m all` with and without that
require and replace; only that module may differ. Every build context that
copies the old `go.mod` also copies `next/go.mod`. Production export is
built from `v1-final`; the rehearsal builds the review commit.

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
| Global and room rules | `goal_rooms.body`, `internal/goals/sqlite.go` | global room `*`; each room's rules |
| Settings | `settings.json`, `internal/settings/settings.go` | only contract fields; minutes to milliseconds |
| Profiles | `profiles.json`, `internal/profile/profile.go` | proxy-supported models; mapping needs review below |
| Hosts | `machines`, `internal/machines/store.go` | names, SSH, labels, room IDs resolved to names; selection needs review |
| Accounts | `accounts.metadata`, `account_repo_scopes`, `internal/account/store` | provider, label, repository scope; never credentials |
| Devices | `web_devices` and scope grants, `internal/websvc/devicestore`, `internal/devicesvc` | token hash, name, dates, scopes; never tokens |
| Repos and room metadata | room records, repository settings, `internal/room/room.go` | resolve repository paths to repository identities; preserve default |
| Skills | skill registry and `SKILL.md` files | installed package reference or owner body |
| Subscriptions | hook documents, `internal/hooks`, script files | only supported Run hooks; exclude built-in timers, librarian and updater; conversion needs review |
| Goals and items | `goals`, `goal_items`, `internal/goals/sqlite.go` | open goals and all their items; no holder |
| Issues and next number | `room_issues`, `room_issue_numbers`, room JSONL feed; `internal/room/issues.go` | open and goal-linked; retain number, body and notes |
| PRs | `pr_records` and record parts, `internal/pr/state` | open tracked PRs; branch and ownership only |
| Questions | `questions`, `internal/questions` | unanswered and not withdrawn; include pending terminal requests |
| Handoff | session store, `internal/subagent/store.go`, room feed and Git refs | open sessions only; resolve parent and goal labels; last post capped at 2,000 characters |

The contract does not count profiles, repositories or skills. Their lengths
should still appear in the report, separately from `BundleCounts`.

## Approved mapping decisions and remaining inventory

The lead approved the following additive bundle fields and mapping decisions. Do not silently discard or invent data.

* The contract has no room manifest. A missing room file cannot be detected
  from a directory alone. Add an explicit room-name list to `GlobalBundle`
  and reject missing, extra, duplicate or name-mismatched room files. The manifest is now part of the contract.
* A terminal request has `Argv`, `Cwd`, `Reason`, `Stage` and `Holder`,
  but the bundle has only `Command`. A joined command loses argv boundaries
  and cwd. Carry argv, cwd and reason; route unanswered requests to root.
* Hook `Command`, `Cwd`, `Timeout`, `OnFail` and script dependencies do
  not directly supply `Run.Image`, `Run.Size` and `Run.Files`. Define
  no Run mapping is invented. Report unmapped active hooks and include them in the handoff.
* Hosts have `Enabled` and `Local`, neither in the bundle. Profiles have
  `Disabled`, `Harness` and `Uses`, and provider model IDs may differ.
  Account `RoomID` has no bundle field. Set explicit selection and mapping
  policy before export. Device scope grants need a precise conversion.
* Rule `SetterName` and `Edits`; goal `Holders`, `Shared`, `NextItem`,
  `ItemsClosed`, `State`, `UpdatedAt`, `Edits`, `Done`,
  `ExpediteUntil`, `LegacyOwnerHeld`; item `UpdatedAt`; question `To`,
  `Post`, `State`, `Answer`, `Withdrawn`, `GroupKey`, `AskedTurn`
  and `AnswerDelivered` have no direct bundle home. Many describe dropped
  history or live v1 execution, but that classification needs review.
  Machine probe, resource, grant and scheduling fields also have no home.
* Source `Commit`, `Backup`, `BackupSHA256` and export time are generated,
  not record fields. Subscription image, size and generated file paths likewise
  need policy. An exhaustive per-type field-loss inventory and exact SQL
  predicates are still required before approval; the table above is not that
  inventory.

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
bundle. Run checks on hermeslab under r19.

## Estimates

Product lines, excluding tests and third party: `cmd/dyna-export` about
900–1,300; `next/migrate` about 200–300; `next/cmd/dyna` about 30–50;
`next/contract` about 5–15 for the manifest. Estimates exclude any policy
or contract expansion above. Exporter and verifier share the first package
but not selection/count code. The exporter PR uses the v1 merge queue; the
dry-run PR touches only `next/`.
