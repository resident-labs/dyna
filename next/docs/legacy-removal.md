# Legacy removal: what is left

The v1 transfer finished (owner, 2026-11-01), and no v1 code is left. Tag `v2-import-last` is the last commit with it,
and `floor.go` names it. What remains is outside the code: leftovers on hosts and the v1 data. Delete this file when both
tables are empty.

## On live hosts

Nothing here has been changed. State as of 2026-11-01.

| Stopgap | State | Removal |
|---|---|---|
| hermeslab `dyna-horizon-watch.timer` | active, every 15 minutes, for the fork mirror alone (`fork-sync.sh`) | Stop it when the fork mirror has a home, which needs a decision. |
| hermeslab `~/.local/share/dyna-next-watch` | present: `bin/`, `state/`, `run/`, `horizon.git`, `backup-before-subscriptions-20261101`, `subscriptions.before-filters` | Delete once the timer is gone. |
| hermeslab `~/shared-folder-move.sh` | present, a copy of the removed script | Delete. |
| hermeslab `dyna-7daa4abfd32e.slice`, `dyna-db8f272fb5d8.slice`, `dyna-needsproof.slice` | active user slices with no tasks; no box row names them | Stop them. They go at the next reboot anyway. |
| Each host's `rooms/<room>/shared` | empty on hermeslab (4 rooms), pve and arch (2 each); no `shared.before-move` on any of them; `lt-24-005` not checked | Delete the empty folders. |
| hermeslab `~/port-bundle` | present; a bundle holds device token hashes | Owner's OK. |
| hermeslab `~/.local/share/dyna-next/backups/port-before-*` | present: `port-before-20261101-053450.db`, `-shm`, `-wal` | Delete when the transfer has proved good. |
| `/home/pip` host data path | present on arch and pve | Goes when host data can differ per host. Hosts still need the same path today. |
| Desktop `dyna computers`, `dyna engine tunnel` | enabled user units `dyna-computers.service` and `dyna-tunnel.service` | Confirm that nothing in v2 needs them, then stop and disable both units. |
| Worktrees of this repository on the desktop | hundreds, some in use by running agents | With the owner's OK, after checking each for unpushed work. |

## v1 data

Each of these needs the owner's explicit OK.

| Item | State | Removal |
|---|---|---|
| Desktop `~/.local/share/dyna`, `~/.config/dyna` | present | After the first good week. First copy out what still gets read there: the node list (`machines`) and the owner's rules (`goal_rooms`). |
| Desktop `~/.cache/dyna-port/` | present: `tools/`, and `v1-snapshot/` with a private v1 copy | With the above. |
