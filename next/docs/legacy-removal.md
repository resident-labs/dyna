# Legacy removal checklist

Code and docs that exist only for v1, the v1-to-v2 transfer, or older v2 data carry this comment:

```
LEGACY(v1-transfer): <what it is>. Remove after the transfer.
```

`git grep -n 'LEGACY(v1-transfer)'` finds all 41 sites, in 40 files, and this file. Each is listed below, with paths
relative to `next/`, apart from `install.sh`, `README.md` and `ci.yml`. Removal starts when the owner says the transfer
finished smoothly, and not before. Live numbers are from 2026-11-01; check them again.

## Conditions

- **Upgrade floor (r17).** An import, migration shim or legacy reader may go once it has shipped for 30 days. Before
  the first release (owner, 2026-10-30) it may go sooner, once `engine/store/floor.go` names the main commit that ran
  it and every live store of the owner has passed it. The rule's text says `internal/engine/stores/floor.go`, which
  was the v1 path. Never remove an import without this. If a release comes first, the 30 days apply.
- **The guard stays (spec 8.4).** `store_from_v1` is not marked and is never removed. Only its text changes.
- **v1 data needs the owner's explicit OK.** That covers anything holding v1 data, as listed under Stopgaps.
- **hermeslab is behind main.** Its store is at schema 61 and main is at 64, because #1914 and #1915 are not deployed.
  Steps 61 to 63 have not run there, so the floor cannot pass 61 until they have.

Read the live version on hermeslab with:

```sh
sqlite3 -readonly ~/.local/share/dyna-next/store.db "select value from meta where key='schema_version'"
```

## Decide first

- [ ] **The real import does not exist.** `dyna import` only takes `--dry-run`, and the rooms were rebuilt by hand, so
  spec 8.4's "after the owner's installs import" never happens literally. The owner decides whether any other v1
  install needs an import. If one does, build it before deleting `migrate/`. If none does, step (4) of the guard text
  and the `v2-import-last` tag need rethinking, since the tag would hold only a dry run.
- [ ] **Which commit is `v2-import-last`.** Spec 8.4 says to tag "that commit", but a commit that deletes `migrate/` cannot
  be built for import. Tag the last commit that still has `migrate/`.
- [ ] **The v1 archive.** Spec 8.1 and section 9 keep v1 room history in `archive/`, and no such folder was written.
  The owner decides whether one is still wanted. If it is, Group C stays. Never delete an `archive/` that exists.

## Order

1. [ ] The owner says the transfer finished smoothly.
2. [ ] Deploy main to hermeslab and confirm the store has reached main's schema version.
3. [ ] Move Horizon's watchers with `docs/horizon-watchers.md`, then stop the timer (Stopgaps).
4. [ ] Settle "Decide first".
5. [ ] Tag `v2-import-last` on the last commit with `migrate/`. In `floor.go`, set `floorVersion` to that commit's
   schema version and `floorCommit` to its full hash. `TestFloor` in `engine/store/store_test.go` writes version 41, so
   it follows the new floor.
6. [ ] Remove Group B in one PR, after the floor change or with it.
7. [ ] Remove Group A in one PR, with the guard text change.
8. [ ] Remove Groups C and D.
9. [ ] Clear the stopgaps.
10. [ ] With the owner's explicit OK, remove the v1 data.
11. [ ] Delete this file, and check that `git grep 'LEGACY(v1-transfer)'` finds nothing.

## Group A: the bundle and the import

Remove together. `v1-final` keeps its own `next/contract`, so the frozen exporter does not depend on main.

`contract/bundle.go` also holds live types, so do not delete the file whole. Move these to their own contract files
first: `AccountProvider`, `CIMode`, `RoomRepo`, `RoomLimits`, `SubscriptionScope`, `Subscription`, `RunSpec`, `RunFile`
and their constants. The engine, CLI and web use them. Everything else in the file is used only by this group.

| Marked at | What | Also change |
|---|---|---|
| `migrate/doc.go` | the package: `doc.go`, `dryrun.go`, `dryrun_test.go` | `docs/spec.md` section 2 layout and dependency text |
| `cmd/dyna/import.go` | `dyna import` | |
| `cmd/dyna/main.go` | registers it | |
| `contract/bundle.go` | bundle types, `DecodeBundle` | keep the live types above; run `go generate ./contract`, since `web/src/lib/contract.gen.ts` holds the bundle types |
| `contract/bundle_test.go` | bundle tests | |
| `contract/bundle_golden_test.go` | golden sample | delete `contract/testdata/v1bundle/` |
| `deps_test.go` | the `migrate` row | |
| `layout_guard_test.go` | the `migrate/dryrun.go` entry | |
| `docs/migrate.md` | the guide | move links to the tag: the guard text, `README.md` ("Moving from v1"), `install.sh` |
| `docs/spec.md` | sections 8.1 to 8.3 and half of 8.4, plus 3.6 | |
| `install.sh` | the v1 lines of the message | the stub stays until v2 has releases, and `ci.yml` still checks that it fails |

Rewrite the guard text in `engine/store/store.go` first, so that it points at `v2-import-last`.

## Group B: older v2 data

All of these wait for the floor. Each row can go alone, but one PR is simpler. Keep the tests' other cases.

| Item | Marked at | Depends on it | Live, hermeslab |
|---|---|---|---|
| Steps 42 to 63 | `engine/store/steps.go`, `steps_test.go`, `global_operations_test.go`, `engine/subscriptions_test.go` | any store below 64. Delete only steps below the new floor, and keep one test from the floor to `schema.sql`. Later steps are ordinary. | schema 61: not yet |
| `shell.job.ended` without `End` | `room/delivery_render.go` and its test, `web/src/lib/jobs.ts`, `jobs.test.ts`, `ui/JobOutput.svelte` | saved events and box agents without `End`. First make the engine fill `End`, in `engine/shelljobs.go`, as the note in `delivery_render.go` says. | 359 events, none without `end` |
| `agentSurface` in `profiles.json` | `engine/profiles.go`, `profiles_test.go` | a profiles file with the key: it will then be refused as unknown | not in the file |
| Several parentless sessions | `room/tree.go`, `tree_test.go` | M1 trial stores | no such room |
| Transcripts before the turn index | `engine/transcript_read.go` and its test, `engine/transcript.go`, `engine/store/transcript_read.go` (2 sites) | files with no `transcript_turns` rows. Goes as one unit. | 37 files, all indexed |
| Checkout with no setup state | `box/agent/repo.go`, `repo_test.go` | a box volume from before `bcdf5dc7b6` (2026-10-31). Without this, setup fails on it. | store cannot show this; check old boxes |
| Old nested parent slices | `engine/lifecycle/steps.go`, `engine/runtime/boxes.go`, `engine/image_wake_test.go`, `engine/runtime/boxes_test.go` | a box whose stored slice is not `box.SliceName`. Also drop the `Recreate` argument in `lifecycle/adapter.go` and the fakes. | 39 boxes, none old |

## Group C: the v1 archive folder

Only if the owner confirms that no archive is wanted, and `<data>/archive/` is absent. It is absent on hermeslab.

| Marked at | What | Also change |
|---|---|---|
| `box/layout.go` | `Layout.Archive` and its line in the layout comment | `box/layout_test.go`, `"archive"` in `layout_guard_test.go`, `docs/spec.md` 3.6, 4.3, 8.1, 9 |
| `engine/runtime/backup.go` | the `Archive` backup root | `docs/backup.md` |

## Group D: other docs

| Marked at | What | When |
|---|---|---|
| `docs/horizon-watchers.md` | the watcher move | after order step 3 |
| `docs/shared-folder-move.md` | the move of Horizon's folders to the central copy | once the owner has confirmed it and the old folders are gone |
| `docs/oauth-import.md` | v1 sign-in mapping, closing warning | when no v1 sign-in is left to move |
| `docs/m1-run.md` | v1 job runner hook | any time after the transfer |

## Stopgaps outside the code

Checked 2026-11-01, except where noted.

| Stopgap | State | Removal |
|---|---|---|
| hermeslab `dyna-horizon-watch.timer` | active, runs `run-all.sh` every 15 minutes | Disable after `docs/horizon-watchers.md`. It stays for the fork mirror until that has a home. |
| hermeslab `~/.local/share/dyna-next-watch` | present, with `bin/`, `state/`, `horizon.git` | Delete once the timer is gone and state lives in `/box/shared/watch`. |
| `fork-sync.sh` | not written yet; the runbook creates it, and the mirror is still in `upstream-watch.sh` | Goes when the fork mirror moves off the host, which needs a decision. |
| Each host's `rooms/<room>/shared.before-move` | none yet; `docs/shared-folder-move.md` makes them | Owner's OK, once the central copy has proved good. |
| `/home/pip` host data path | present on arch and pve; `lt-24-005` not checked, because the login was refused | Goes when host data can differ per host. Hosts still need the same path today. |
| Desktop `~/.cache/dyna-port/` | present: `tools/`, and `v1-snapshot/` with a private v1 copy | Owner's OK: it holds v1 data. |
| hermeslab `~/port-bundle` | present; a bundle holds device token hashes | Owner's OK. |
| pve `~/port-next`, `~/port-rehearsal` | gone with the rebuild | Nothing to do. |
| hermeslab `~/.local/share/dyna-next/backups/port-before-*` | present: `port-before-20261101-053450.db`, `-shm`, `-wal` | Delete when the transfer has proved good. |
| Desktop `dyna computers`, `dyna engine tunnel` | running, from the enabled user units `dyna-computers.service` and `dyna-tunnel.service` | Confirm that nothing in v2 needs them (spec 8.3 step 8 reuses the tunnel), then stop and disable both units. |
| Desktop `~/.local/share/dyna`, `~/.config/dyna` | present; not moved to `~/.local/share/dyna-v1/` (spec 8.3 step 9) | Owner's explicit OK, after the first good week. First copy out what still gets read there: the node list (`machines`) and the owner's rules (`goal_rooms`). |
| Worktrees of this repository on the desktop | 463 in `git worktree list`, some in use by running agents | With the owner's OK, after checking each for unpushed work. |

## Looked at and not marked

- **Stays:** the `store_from_v1` guard (`store.go`, `Layout.V1Store`, `ErrStoreFromV1`, `TestV1Guard`), the `dyna-v2`
  format check, `floor.go`, `upgrade()`, `schema.sql` and `store_too_old`. Future schema steps are ordinary migrations.
- **Ordinary:** `ContainerLayout` and `StaleLayout` (any later layout change uses them), `ErrUnknownBoxMessage` (box
  and engine skew on any upgrade), `/v1/` API paths and the `dyna-connect-*.v1` strings (v2's own), and `auth-v2`.
- **Not about the transfer:** `engine/live/channel.go`'s transitional lock and `TranscriptStep` in `harness/pi`, which are
  v2 internals, and the `third_party` trees.
- **Merely old:** comments saying "as in v1", which only record where behaviour came from. `README.md` line 13, the
  note at the top of `scripts/deploy-trial.sh`, `contract/ids.go` and `engine/model/proxy.go` only mention v1. Tidy them
  with Group A.
- **`.github` and `.dyna`:** nothing here exists only for v1.
- **Owner's call:** `accounts.import_oauth` came in for moving v1 sign-ins, but it is a general owner method and
  its doc says so, so only its v1 paragraph is marked. The `dyna-next` names (default data folder, socket folder,
  unit, `deploy-trial.sh`) and spec 8.3 step 7, moving `next/*` to the root, are renames rather than removals.
