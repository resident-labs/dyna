# Moving Horizon to the central shared folder
<!-- LEGACY(v1-transfer): this whole runbook, one move of Horizon's per-host folders. Remove after the transfer. -->

`/box/shared` is now one central copy per room (spec 5.6). This moves Horizon's existing folders into it, once. Run it by
hand. Nothing here is automatic.

Each host's folder, as of 2026-11-01 (`du --apparent-size`):

| Host | Folder | Holds |
|---|---|---|
| hermeslab | 1.6 GB | `horizon-local` 1.0 GB, `watch` 380 MB (`horizon.git` 345 MB, `bin/gh` 36 MB), `api-lead` 37 MB, `github` 11 MB |
| pve | 1.07 GB | `horizon-local` 1.0 GB, `api-lead` 16 MB |
| arch | 174 MB on disk | `horizon-local` 1.0 GB apparent, `api-lead` 1.2 MB |
| lt-24-005 | not measured on disk | `horizon-local` 1.0 GB apparent, an empty `api-lead`, nothing else |

Only `horizon-local` is big, and every host already has it. So it moves within each host, and the merge carries little
over the 2.4 MB/s links to pve and arch. hermeslab has 584 GB free.

## 1. Before the deploy: the hosts

Do this first. Once this engine runs, a box cannot start on a host without FUSE: it stays stopped and says why. A box
that is already running carries on, on its old folder.

| Host | `fusermount3` | `/dev/fuse` | `user_allow_other` |
|---|---|---|---|
| arch | present | present | commented out, line 10 |
| pve | present | present | commented out, line 10 |
| lt-24-005 (WSL2) | missing | present | no `/etc/fuse.conf` yet |
| hermeslab | present | present | not needed: the engine host has no mount |

```sh
# lt-24-005 only
sudo apt-get install -y fuse3

# arch, pve and lt-24-005. Match the bare line only: line 3 of this file is a comment that starts with the same words,
# and fusermount3 refuses a mount if that one is uncommented.
sudo sed -i 's/^#user_allow_other$/user_allow_other/' /etc/fuse.conf
grep -q '^user_allow_other$' /etc/fuse.conf || echo user_allow_other | sudo tee -a /etc/fuse.conf

# check each one
command -v fusermount3 && ls -l /dev/fuse && grep -n '^[^#]' /etc/fuse.conf
```

## 2. Stop what writes to the old folders

```sh
export XDG_RUNTIME_DIR=/run/user/$(id -u)
sock=$XDG_RUNTIME_DIR/dyna-next/owner.sock
dyna() { ~/.local/opt/dyna-next/bin/dyna --socket "$sock" "$@"; }
room=room_01M6BE8K3XG5D18Z259VVYTSAW          # Horizon
data=~/.local/share/dyna-next                 # hermeslab
host=/home/pip/.local/share/dyna-next-host    # every host
central=$data/rooms/$room/shared

dyna room pause "$room"
```

Do this before the deploy. From the deploy on, a box that starts gets the new folder, which is empty until step 4, and a
paused room starts nothing, its Runs included. Boxes go dormant when quiet, or close them. Before going on, this must
print nothing on hermeslab, pve, arch or lt-24-005:

```sh
podman ps --format '{{.Names}}' | xargs -r podman inspect --format '{{.Name}} {{range .Mounts}}{{.Source}} {{end}}' | grep "/rooms/$room/shared"
```

## 3. Deploy

Deploy as usual. The container layout changes, so each box is recreated at its next start, with its volume intact.
A box that keeps running stays bound to its old folder until then. The engine asks each host for its mount within 10
seconds of reaching it.

The mount has been run on pve only. arch and lt-24-005 are unproven, so check that each has one before going on:

```sh
systemctl --user status dyna-share.service
mountpoint /home/pip/.local/share/dyna-next-host/share/rooms
journalctl --user -u dyna-share.service -n 20    # why, if it is not there
```

The server is `dyna-share-server.service` on hermeslab. A deploy leaves it and the mounts running on the old binary, and
that is what keeps running boxes bound to them.

## 4. Merge into the central copy

Start from hermeslab, which holds what the others do not. `horizon-local` and `watch/horizon.git` stay out, for step 5.
Each host's old folder is only copied from, and it is renamed afterwards, so nothing is lost until the owner says so.

```sh
# on hermeslab
mkdir -p "$central"
rsync -a --exclude /horizon-local --exclude /watch/horizon.git "$host/rooms/$room/shared/" "$central/"
```

Then the other two, newest file wins. Look at the dry run first: any file that appears as changed exists on both sides
with different content, and the owner decides those. Run this from a machine that can reach both hosts, as hermeslab can
reach pve and arch.

```sh
for login in build@fl-rea-pve aria@arch; do
  rsync -ani --update --exclude /horizon-local "$login:$host/rooms/$room/shared/" "$central/" | grep -v '/$' | tee "/tmp/merge-${login#*@}.txt"
done
# once the lists are agreed
for login in build@fl-rea-pve aria@arch; do
  rsync -a --update --exclude /horizon-local "$login:$host/rooms/$room/shared/" "$central/"
done
```

lt-24-005 holds only `horizon-local`, the setup script and `briefs`, so it adds nothing. Check that with the same dry run
if in doubt.

## 5. Move the heavy files to /box/cache

`/box/cache` is per host, and a Run gets it too, so each host keeps its own copy. `horizon-local` moves within each host,
with no copying. `<hostdata>/cache` is shared by every room on a host, and it has no size limit.

```sh
# on hermeslab, pve, arch and lt-24-005, with room and host set as in step 2
mv "$host/rooms/$room/shared/horizon-local" "$host/cache/horizon-local"
```

The watchers' bare clone goes the same way on hermeslab. On any other host, the first Run there makes its own, which
fetches about 345 MB:

```sh
mv "$host/rooms/$room/shared/watch/horizon.git" "$host/cache/horizon-watch.git"     # hermeslab
```

## 6. Point everything at the new paths

- **`horizon-setup.sh`** (in the central copy): set `data=/box/cache/horizon-local`. Existing checkouts link to the old
  path, so each box runs `sh /box/shared/horizon-setup.sh` again. A new host has no copy, and the script says so.
- **Room instructions**, `$data/rooms/$room/AGENTS.md`:
  - line 38 says `/box/shared` is per host, and to use sameHost for a child that needs another session's files. It is now
    one folder on every host, and sameHost is only for a child that has to be on the same machine.
  - line 40 points at the setup script, which stays where it is.
  - line 56 says the watchers keep their state in `/box/shared/watch` on hermeslab. Drop "on hermeslab".
  - add the rules from spec 5.6: last write wins, locks stop at a host, 5 seconds to show on another host, and heavy
    rebuildable files go in `/box/cache`.
- **The watchers.** A subscription keeps its own copy of `env.sh`, and there is no edit, so remove the six and add them
  again from `docs/horizon-watchers.md` steps 2 and 5, with `repo_dir=/box/cache/horizon-watch.git` as that page now has
  it. `dyna subscription list --room "$room"` names them. A Run can now land on any host, and `flock` in `env.sh` only reaches one host, so two watchers can run at once.
  Each keeps its own state file, so that only costs a second `git fetch`. Tell root if that matters.
- **Host commands.** Root has run scripts by path with `hostexec run`, such as
  `$host/rooms/$room/shared/github/tmp-stats.sh`. On hermeslab that path is now `$central/github/tmp-stats.sh`.

## 7. Rename the old folders and resume

```sh
# on hermeslab, pve, arch and lt-24-005. The empty folder lets an old stopped box start once to archive, as a closing box
# is not recreated first.
mv "$host/rooms/$room/shared" "$host/rooms/$room/shared.before-move" && mkdir "$host/rooms/$room/shared"

# on hermeslab
dyna room resume "$room"
```

Each box comes back through a recreate. Then check, on a box on another host than hermeslab:

```sh
echo hello > /box/shared/move-check     # the file appears in $central/move-check within about 3 seconds
ls /box/cache/horizon-local             # the vanilla data
```

## 8. When the owner confirms

Remove `$host/rooms/$room/shared.before-move` and the empty `shared` on each host. Deleting the room would also remove them, because they sit in
its host folder. Then remove this page and its row in `docs/legacy-removal.md`.
