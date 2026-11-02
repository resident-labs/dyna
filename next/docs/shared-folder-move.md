# Moving Horizon to the central shared folder
<!-- LEGACY(v1-transfer): this whole runbook, one rolling move of Horizon's per-host folders. Remove after the transfer. -->

`/box/shared` is now one central copy per room (spec 5.6). This moves Horizon's folders into it while its boxes keep
running. There is no room pause. Run it by hand, on hermeslab, as `pip`. `scripts/shared-folder-move.sh` does every copy:
each command can run again, and none deletes a file.

```sh
export XDG_RUNTIME_DIR=/run/user/$(id -u)
move() { bash ~/shared-folder-move.sh room_01M6BE8K3XG5D18Z259VVYTSAW "$@"; }    # copy the script from next/scripts first
```

It finds the hosts in the store, so it needs `sqlite3` on hermeslab, and `rsync` on every host. `move check` says what
each host lacks. As of 2026-11-01, only Horizon's folders hold anything, and every host already has its own 1.0 GB
`horizon-local`, so the copies to pve and arch carry little over their 2.4 MB/s links. The other three rooms' folders are
empty on every host, so they need no copy.

## 1. Set up the hosts

Do this first. Once the new engine runs, a box cannot start on a host without FUSE: it stays stopped and says why.

```sh
move check
move setup    # fuse3 on lt-24-005 with pacman, and user_allow_other on arch, lt-24-005 and pve; asks for sudo
move check    # prints no "is missing" and no "is not set"
```

| Host | `fusermount3` | `/dev/fuse` | `user_allow_other` |
|---|---|---|---|
| arch | present | present | commented out, line 10 |
| pve | present | present | commented out, line 10 |
| lt-24-005 (WSL2, Arch) | missing | present | no `/etc/fuse.conf` yet |
| hermeslab | present | present | not needed: the engine host has no mount |

## 2. Disable the six watchers

```sh
move watchers-off
```

They stay disabled until step 7, so nothing writes to their clone while it is copied. `move watchers-undo` turns them
back on as they were.

## 3. Pre-copy, with boxes running

```sh
move plan     # lists what would change, and changes nothing
move copy
```

`copy` does this on every host, and only adds or replaces with a newer file:

- It copies the host's folder to `~/.local/share/dyna-next/rooms/<room>/shared`. On the same path, the newest file wins,
  on whichever side it is. `horizon-local` and `watch/horizon.git` stay out.
- It copies `horizon-local` to `<hostdata>/cache/horizon-local`, on each host that has it.
- It copies `watch/horizon.git` to `<hostdata>/cache/horizon-watch.git`, on each host that has it, which is hermeslab.

The originals stay where they are, so running boxes keep working. Run `move copy` as often as you like.

Then replace `horizon-setup.sh` (step 6). It works on either layout, because `/box/cache` is the same on both.

## 4. Deploy

Deploy as usual. The container layout is now 4, so each box is recreated at its next start, with its volume intact, and
binds the new folder then. A deploy also changes the box agent, so a box stops as soon as its session needs no box, and
comes back on the new layout (#1934). Moved boxes see the central copy, and the others keep their old folder.

The engine asks each host for its mount within 10 seconds of reaching it. The mount has been run on pve only, and arch and
lt-24-005 are unproven, so check them now:

```sh
ssh aria@arch 'systemctl --user is-active dyna-share.service; mountpoint /home/pip/.local/share/dyna-next-host/share/rooms'
ssh afigueredo@lt-24-005 'systemctl --user is-active dyna-share.service; mountpoint /home/pip/.local/share/dyna-next-host/share/rooms'
journalctl --user -u dyna-share-server.service -n 5    # the server, on hermeslab
```

If a mount is missing, `journalctl --user -u dyna-share.service -n 20` on that host says why. Boxes on it stay stopped
with the reason, and the other hosts carry on.

## 5. Watch the rollout

```sh
move stale
```

This lists every box on an old layout, as `HOST BOX STATE layout=N old-folder=yes|no`. It exits 0 and says `no running
box is on the old layout` once the rollout has finished. A stopped box may stay on the list: it moves when it next starts,
and it uses no folder until then. To hurry the rest, `~/.local/opt/dyna-next/bin/dyna session refresh --room <room>` marks
each running box to restart once no turn, job or terminal needs it, with its volume kept.

During the rollout, the old folders and the central copy each take new writes. `move copy` brings the old folders' side
across at any time. A file deleted in the central copy comes back if an old folder still has it, because a copy never
deletes.

## 6. Replace horizon-setup.sh

This is live data, so it is not in this repository. Write this to `/box/shared/horizon-setup.sh` in the central copy,
`~/.local/share/dyna-next/rooms/<room>/shared/horizon-setup.sh`:

```sh
#!/bin/sh
# Prepare a Horizon checkout in a box: link the vanilla data into .local. Replaces v1's worktree-setup hook.
# Run it from the checkout, once, and again if a link stops working. .local stays a real folder, so each box keeps its own
# parity-target output.
set -eu
data=/box/cache/horizon-local
[ -d "$data" ] || { echo "no vanilla data at $data: /box/cache is per host" >&2; exit 1; }
mkdir -p .local
for entry in "$data"/*; do
  ln -sfn "$entry" ".local/$(basename "$entry")"
done
echo "linked $(ls "$data" | wc -l) folders into .local"
```

Only the `data=` line, its error and the comment change. Its mtime is newest, so no later copy overwrites it.

Tell root this. Links in a checkout's `.local` point at `/box/shared/horizon-local/...`, and they sit in the box's volume,
so they survive the move and then dangle, because `horizon-local` is not in the central copy. A running box on its old
folder is fine until it moves. Each session runs this once after its box has moved, from its checkout:

```sh
sh /box/shared/horizon-setup.sh
```

`ln -sfn` replaces each old link. A session with more than one checkout can fix them all from anywhere in its box:

```sh
find /box -xdev -type l -lname '/box/shared/horizon-local/*' -exec sh -c 'for link; do ln -sfn "/box/cache/horizon-local/$(basename "$link")" "$link"; done' sh {} +
```

Then update the room instructions, `~/.local/share/dyna-next/rooms/<room>/AGENTS.md`:

- Line 38 says `/box/shared` is per host, and to use sameHost for a child that needs another session's files. It is now
  one folder on every host, and sameHost is only for a child that has to be on the same machine.
- Line 56 says the watchers keep their state in `/box/shared/watch` on hermeslab. Drop "on hermeslab".
- Add the rules from spec 5.6: last write wins, locks stop at a host, 5 seconds to show on another host, and heavy
  rebuildable files go in `/box/cache`.

Root has also run scripts by host path with `hostexec run`, such as
`<hostdata>/rooms/<room>/shared/github/tmp-stats.sh`. On hermeslab, that path is now under
`~/.local/share/dyna-next/rooms/<room>/shared/`.

## 7. Finish

Once `move stale` says the rollout has finished:

```sh
move finish
move watchers-on hermeslab
```

`finish` refuses while a running box uses an old folder. Otherwise it copies anything newer once more, then renames each
host's folder to `shared.before-move`, leaving an empty `shared` so that an old stopped box can still start once to
archive. Running it again changes nothing.

`watchers-on` adds each disabled Run again with `--host hermeslab`, and with `repo_dir=/box/cache/horizon-watch.git` in
its `env.sh`, then removes the old one. The pin keeps `flock` in `env.sh` working, because a lock stops at a host. It needs
the new binary, so it comes after the deploy. A schedule first fires one period after it is added.

## 8. When the owner confirms

Check from a box on a host other than hermeslab:

```sh
echo hello > /box/shared/move-check     # appears in the central copy within about 3 seconds
ls /box/cache/horizon-local             # the vanilla data
```

Then remove `<hostdata>/rooms/<room>/shared.before-move`, the empty `shared` and the old `horizon-local` inside it on each
host. Deleting the room would also remove them, because they sit in its host folder. Then remove this page, the script
and its test, and their row in `docs/legacy-removal.md`.

## Rollback

- **Before the deploy,** nothing has changed for a box: every copy is unused. Run `move watchers-undo` and stop.
- **After the deploy,** fix forward if you can. Nothing is lost at any point: each old folder stays whole until `finish`,
  and after it under `shared.before-move`.

To go back to the old engine:

1. Stop the engine. It stays stopped until step 3, because the old binary refuses a store at schema 70.
2. Run `move rollback`. It undoes schema step 69, which renames `run_host` back to `last_host` and rebuilds the sizes table
   empty. It renames each `shared.before-move` back, and copies the central copy to each host's old folder, newest wins.
3. Start the old binary, then run `move watchers-undo`. Runs added with `watchers-on` are ordinary subscriptions: remove
   them and add the old ones if they were already replaced.

A box on layout 4 reads as stale to the old engine, so it goes back to its old folder at its next start. Each still-running
box stays on the central copy or its mount until then, and `dyna-share.service` and `dyna-share-server.service` stay up for
it. Stop them with `systemctl --user stop` once no box binds them. Sessions that ran `horizon-setup.sh` keep working,
because `/box/cache` is the same under either engine.
