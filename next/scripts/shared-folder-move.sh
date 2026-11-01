#!/usr/bin/env bash
# LEGACY(v1-transfer): this whole script, one rolling move of a room's per-host folders. Remove after the transfer.
# Moves a room's shared folders to the central copy while its boxes run. docs/shared-folder-move.md says when to run what.
# Run it on the engine host. Every command can run again, and none deletes a file: copies only add or replace with a newer
# file, and old folders are renamed.
#
#   shared-folder-move.sh ROOM check          what each host lacks; changes nothing
#   shared-folder-move.sh ROOM setup          install fuse3 and set user_allow_other where missing; asks for sudo
#   shared-folder-move.sh ROOM watchers-off   disable the room's Runs
#   shared-folder-move.sh ROOM plan           list what copy would change; changes nothing
#   shared-folder-move.sh ROOM copy           copy every host's folder to the central copy, and the heavy folders to /box/cache
#   shared-folder-move.sh ROOM stale          list the boxes still on the old layout; exits 1 while one is running
#   shared-folder-move.sh ROOM finish         once no running box uses an old folder: copy once more, then rename the old folders
#   shared-folder-move.sh ROOM watchers-on H  add the disabled Runs again, pinned to host H, reading the moved clone
#   shared-folder-move.sh ROOM watchers-undo  enable the disabled Runs as they were
#   shared-folder-move.sh ROOM rollback       with the engine stopped: put the store and the old folders back
set -euo pipefail

DATA=${DATA:-$HOME/.local/share/dyna-next}
HOST_DATA=${HOST_DATA:-/home/pip/.local/share/dyna-next-host}
DYNA=${DYNA:-$HOME/.local/opt/dyna-next/bin/dyna}
SOCKET=${SOCKET:-${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/dyna-next/owner.sock}
LAYOUT=4 # box.ContainerLayout
HEAVY=horizon-local
CLONE=watch/horizon.git
CLONE_CACHE=horizon-watch.git
STORE=$DATA/store.db

die() { echo "$*" >&2; exit 1; }
[ $# -ge 2 ] || die "usage: $0 ROOM check|setup|watchers-off|plan|copy|stale|finish|watchers-on HOST|watchers-undo|rollback"
room=$1 command=$2
case "$room" in *[!A-Za-z0-9_-]*|'') die "invalid room $room" ;; esac
central=$DATA/rooms/$room/shared
old=$HOST_DATA/rooms/$room/shared
cache=$HOST_DATA/cache

sql() { sqlite3 -readonly "$STORE" "$1"; }
dyna() { "$DYNA" --socket "$SOCKET" "$@" </dev/null; }

command -v sqlite3 >/dev/null || die "sqlite3 is missing"
# all_hosts holds name, login and whether it is the engine host, one host a line. Loops read it on fd 3, so a login keeps stdin.
all_hosts=$(sql "SELECT name,ssh,COALESCE(engine_host,0) FROM hosts WHERE state!='removed' ORDER BY engine_host DESC,name" | tr '|' ' ')
[ -n "$all_hosts" ] || die "the store lists no hosts"

# on runs a script on a host. The engine host's own script runs here, as the engine does not need a login to itself for this.
on() {
  local login=$1 engine=$2 script=$3
  shift 3
  if [ "$engine" = 1 ]; then bash -c "$script" dyna-move "$@" </dev/null; else
    local quoted="" arg
    for arg in "$script" dyna-move "$@"; do quoted+=" $(printf '%q' "$arg")"; done
    ssh -n "$login" "bash -c$quoted"
  fi
}

# rsync 24 means a file went away while it was read, which is what a running box does. The next copy gets the rest.
sync_tree() {
  local status=0
  rsync "$@" || status=$?
  case "$status" in 0|24) ;; *) echo "rsync failed with status $status; run this again" >&2; return "$status" ;; esac
}

# copy_host brings one host's folder to the central copy. The newest file on a path wins, on whichever side it is.
copy_host() {
  local login=$1 engine=$2 source=$old/
  shift 2
  [ "$engine" = 1 ] || source=$login:$old/
  sync_tree -rlptD --update --itemize-changes "$@" --exclude "/$HEAVY" --exclude "/$CLONE" "$source" "$central/" | { grep -v '/$' || true; }
}

# copy_cache copies the heavy folders to the host's /box/cache and leaves them where they are, because running boxes use them.
copy_cache_script='set -eu
old=$1 cache=$2 heavy=$3 clone=$4 clone_cache=$5 dry=$6
copy() {
  [ -d "$1" ] || return 0
  status=0
  rsync -rlptD --update $dry "$1/" "$2/" || status=$?
  case "$status" in 0|24) ;; *) exit "$status" ;; esac
  echo "  $1 ${dry:+would be }copied to $2"
}
[ -n "$dry" ] || mkdir -p "$cache"
copy "$old/$heavy" "$cache/$heavy"
copy "$old/$clone" "$cache/$clone_cache"'

copy_all() {
  local dry=${1:-} name login engine
  [ -n "$dry" ] || mkdir -p "$central"
  while read -r name login engine <&3; do
    echo "== $name"
    if on "$login" "$engine" '[ -d "$1" ]' "$old"; then
      copy_host "$login" "$engine" $dry
      on "$login" "$engine" "$copy_cache_script" "$old" "$cache" "$HEAVY" "$CLONE" "$CLONE_CACHE" "$dry"
    else
      echo "  no folder"
    fi
  done 3<<<"$all_hosts"
}

# stale_script prints each container with its state, its layout, and whether it binds the room's old folder.
stale_script='ids=$(podman ps -aq --filter label=dyna.box; podman ps -q)
[ -n "$ids" ] || exit 0
for id in $ids; do echo "$id"; done | sort -u | xargs podman inspect --format "{{.Name}} {{.State.Status}} layout={{index .Config.Labels \"dyna.layout\"}} {{range .Mounts}}{{.Source}} {{end}}" | while read -r name state layout mounts; do
  bound=no
  case " $mounts " in *" $1 "*) bound=yes ;; esac
  echo "$name $state $layout old-folder=$bound"
done'

# stale_list prints HOST NAME STATE LAYOUT OLD-FOLDER for each box or Run still tied to the old layout.
stale_list() {
  local name login engine
  while read -r name login engine <&3; do
    on "$login" "$engine" "$stale_script" "$old" | awk -v host="$name" -v want="layout=$LAYOUT" '$3 != want && $3 != "layout=" || $4 == "old-folder=yes" {print host, $0}'
  done 3<<<"$all_hosts"
}

check() {
  local name login engine
  while read -r name login engine <&3; do
    echo "== $name"
    on "$login" "$engine" 'for tool in rsync podman; do command -v "$tool" >/dev/null || echo "  $tool is missing"; done
if [ "$2" = 1 ]; then
  echo "  engine host: needs no FUSE"
  if [ -d "$1" ]; then echo "  old folder: $(du -sh "$1" | cut -f1)"; fi
  exit 0
fi
[ -c /dev/fuse ] || echo "  /dev/fuse is missing"
command -v fusermount3 >/dev/null || echo "  fusermount3 is missing"
grep -q "^user_allow_other" /etc/fuse.conf 2>/dev/null || echo "  user_allow_other is not set"
if [ -d "$1" ]; then echo "  old folder: $(du -sh "$1" | cut -f1)"; fi' "$old" "$engine"
  done 3<<<"$all_hosts"
}

setup() {
  local name login engine
  while read -r name login engine <&3; do
    [ "$engine" = 1 ] && continue
    echo "== $name"
    ssh -t "$login" 'set -eu
command -v fusermount3 >/dev/null || sudo pacman -S --noconfirm fuse3
grep -q "^user_allow_other$" /etc/fuse.conf 2>/dev/null || sudo sed -i "s/^#user_allow_other$/user_allow_other/" /etc/fuse.conf
grep -q "^user_allow_other$" /etc/fuse.conf || echo user_allow_other | sudo tee -a /etc/fuse.conf >/dev/null
echo "  $(command -v fusermount3), $(grep "^user_allow_other" /etc/fuse.conf)"'
  done 3<<<"$all_hosts"
}

runs() { sql "SELECT id FROM subscriptions WHERE room='$room' AND run_image!='' AND enabled=$1 ORDER BY id"; }

watchers_on() {
  local pin=$1 id every image size folder event path
  local -a args files on_events
  while read -r id <&4; do
    folder=$(mktemp -d)
    IFS='|' read -r every image size < <(sql "SELECT every_ms,run_image,run_size FROM subscriptions WHERE id='$id'")
    mapfile -t args < <(sql "SELECT arg FROM subscription_args WHERE sub='$id' ORDER BY pos")
    mapfile -t on_events < <(sql "SELECT event_type FROM subscription_events WHERE sub='$id'")
    files=()
    while read -r path <&5; do
      mkdir -p "$folder/$(dirname "$path")"
      sql "SELECT body FROM subscription_files WHERE sub='$id' AND path='$path'" > "$folder/$path"
      sed -i "s#^repo_dir=.*#repo_dir=/box/cache/$CLONE_CACHE#" "$folder/$path"
      files+=(--file "$path=$folder/$path")
    done 5< <(sql "SELECT path FROM subscription_files WHERE sub='$id'")
    local -a when=()
    [ "$every" = 0 ] || when=(--every "${every}ms")
    for event in "${on_events[@]}"; do when+=(--on "$event"); done
    dyna subscription add --room "$room" "${when[@]}" --image "$image" --size "$size" --host "$pin" "${files[@]}" -- "${args[@]}"
    dyna subscription remove "$id"
    rm -rf "$folder"
    echo "  $id is now pinned to $pin"
  done 4< <(runs 0)
}

rename_folders() {
  local name login engine
  while read -r name login engine <&3; do
    on "$login" "$engine" 'set -eu
[ -d "$1" ] || exit 0
if [ -e "$1.before-move" ]; then
  if [ -z "$(ls -A "$1")" ]; then exit 0; fi
  echo "  $1 and $1.before-move both hold files: look before going on" >&2
  exit 1
fi
mv "$1" "$1.before-move"
mkdir "$1"
echo "  renamed to $1.before-move"' "$old"
  done 3<<<"$all_hosts"
}

# rollback puts back what step 69 changed, and each old folder with whatever reached the central copy since.
rollback() {
  [ "$(systemctl --user is-active dyna-next.service || true)" != active ] || die "stop the engine first"
  [ "$(sql "SELECT value FROM meta WHERE key='schema_version'")" = 70 ] || die "this undoes schema 70 only"
  sqlite3 "$STORE" "BEGIN;
ALTER TABLE subscriptions RENAME COLUMN run_host TO last_host;
UPDATE subscriptions SET last_host='';
DROP TABLE room_shared;
CREATE TABLE room_shared (room TEXT NOT NULL, host TEXT NOT NULL REFERENCES hosts(name), bytes INTEGER NOT NULL, measured_at INTEGER NOT NULL, PRIMARY KEY(room, host));
UPDATE meta SET value='69' WHERE key='schema_version';
COMMIT;"
  local name login engine
  while read -r name login engine <&3; do
    echo "== $name"
    on "$login" "$engine" 'set -eu
if [ -d "$1.before-move" ]; then
  rmdir "$1"  # refuses unless it is still the empty folder that finish left
  mv "$1.before-move" "$1"
fi' "$old"
    local target=$old/
    [ "$engine" = 1 ] || target=$login:$old/
    if [ -d "$central" ]; then sync_tree -rlptD --update "$central/" "$target"; fi
  done 3<<<"$all_hosts"
}

case "$command" in
  check) check ;;
  setup) setup ;;
  watchers-off) while read -r id; do dyna subscription disable "$id"; echo "  $id disabled"; done < <(runs 1) ;;
  watchers-on) [ $# -eq 3 ] || die "watchers-on needs a host"; watchers_on "$3" ;;
  watchers-undo) while read -r id; do dyna subscription enable "$id"; echo "  $id enabled"; done < <(runs 0) ;;
  plan) copy_all --dry-run ;;
  copy) copy_all ;;
  stale)
    list=$(stale_list)
    [ -z "$list" ] || echo "$list"
    if echo "$list" | grep -q ' running '; then echo "the rollout has not finished"; exit 1; fi
    echo "no running box is on the old layout"
    ;;
  finish)
    list=$(stale_list)
    if echo "$list" | grep ' running ' | grep -q 'old-folder=yes'; then echo "$list" | grep 'old-folder=yes'; die "these boxes still use an old folder"; fi
    copy_all
    rename_folders
    ;;
  rollback) rollback ;;
  *) die "unknown command $command" ;;
esac
