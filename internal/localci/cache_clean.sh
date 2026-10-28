#!/usr/bin/env bash
# Removes Dyna's own CI caches and the directories of finished runs on one CI
# machine. Dyna shares a CI machine with other work, so this script never
# touches a path outside its allow-list: /var/tmp/dyna-local-ci and its
# /var/tmp/dyna-local-ci-<uid> trees. It never runs as root, stays on one file
# system, and follows no symlink.
#
# Inputs come from the environment, so the caller sends one shell command:
#   DYNA_CI_CLEAN_DRY=1        report what would go, delete nothing
#   DYNA_CI_CLEAN_AGE_MIN=2880     keep caches younger than this many minutes
#   DYNA_CI_CLEAN_RUN_AGE_MIN=120  keep run folders younger than this many minutes
#   DYNA_CI_CLEAN_ROOTS=...        exact CI directories to clean (space separated);
#                              empty cleans every directory Dyna owns
#   DYNA_CI_CACHE_CAP_KB=...   size cap in KiB for every cache, for a caller
#                              that runs this script directly; 0 turns the size
#                              bound off. Defaults: 20 GiB go-build, 10 GiB
#                              go-mod, and 5 GiB for every other cache. A cache
#                              over its cap loses its oldest entries until it fits,
#                              even when they are recent.
#
# A root is the directory a run keeps its caches in, holding runs/,
# diagnostics/ and caches/. The caller passes the one it writes to, which
# names the controller's user ID, so the ID here never has to match it.
#
# It prints one DYNA_CI_CACHE_CLEAN_ITEM <kb> <path> line per path it removes,
# then DYNA_CI_CACHE_CLEAN_FREED_KB with the total, for the run log.
set -u

dry=${DYNA_CI_CLEAN_DRY:-0}
age_min=${DYNA_CI_CLEAN_AGE_MIN:-2880}
run_age_min=${DYNA_CI_CLEAN_RUN_AGE_MIN:-120}
cap_override=${DYNA_CI_CACHE_CAP_KB:-}
legacy=/var/tmp/dyna-local-ci

refuse() { echo "dyna-ci-clean: $*" >&2; exit 2; }

# A runner may hold a cache, but never Dyna's CI directories as root.
[ "$(id -u)" != 0 ] || refuse "refusing to run as root"
case "$dry" in 0 | 1) ;; *) refuse "DYNA_CI_CLEAN_DRY must be 0 or 1" ;; esac
[[ "$age_min" =~ ^[0-9]+$ ]] || refuse "DYNA_CI_CLEAN_AGE_MIN must be a number of minutes"
[[ "$run_age_min" =~ ^[0-9]+$ ]] || refuse "DYNA_CI_CLEAN_RUN_AGE_MIN must be a number of minutes"
[ -z "$cap_override" ] || [[ "$cap_override" =~ ^[0-9]+$ ]] || refuse "DYNA_CI_CACHE_CAP_KB must be a number of KiB"

# allowed is the only guard that keeps a typo from deleting another user's data.
allowed() {
  case "$1" in
  /var/tmp/dyna-local-ci | /var/tmp/dyna-local-ci/* | /var/tmp/dyna-local-ci-?*) return 0 ;;
  esac
  return 1
}

# bases lists the directories to clean, each of them Dyna's own. One run names
# its own, and every other tree under /var/tmp/dyna-local-ci* is a leftover of
# a run on this machine, including one of another user ID.
bases=()
add_base() {
  local base=$1 other
  case "$base" in *..*) return 0 ;; esac
  allowed "$base" || return 0
  for other in ${bases[@]+"${bases[@]}"}; do
    [ "$other" = "$base" ] && return 0
  done
  bases+=("$base")
}

# safe_base BASE: BASE and its Dyna-owned parent are real directories. A linked
# base would point the cleanup at another user's tree, so nothing under it is
# touched. The legacy tree's parent is /var/tmp, which is not Dyna's own.
safe_base() {
  local base=$1 parent
  if [ -L "$base" ]; then
    echo "dyna-ci-clean: refusing linked CI base $base" >&2
    return 1
  fi
  parent=${base%/*}
  if [ "$parent" != "$base" ] && allowed "$parent" && [ -L "$parent" ]; then
    echo "dyna-ci-clean: refusing CI base $base under linked parent $parent" >&2
    return 1
  fi
  return 0
}

# safe_path PATH: PATH resolves inside the allow-list. Nothing may change PATH
# before this passes, because a linked parent can name another user's tree.
safe_path() {
  local real
  real=$(readlink -f -- "$1" 2>/dev/null) || return 1
  [ -n "$real" ] || return 1
  allowed "$real" || {
    echo "dyna-ci-clean: skipping $1, it resolves outside the allow-list" >&2
    return 1
  }
}

if [ -n "${DYNA_CI_CLEAN_ROOTS:-}" ]; then
  read -r -a named <<< "$DYNA_CI_CLEAN_ROOTS"
  for base in "${named[@]}"; do
    case "$base" in *..*) refuse "refusing path with ..: $base" ;; esac
    allowed "$base" || refuse "refusing path outside the Dyna CI allow-list: $base"
    add_base "$base"
  done
else
  add_base "$legacy"
  for tree in /var/tmp/dyna-local-ci-*; do
    [ -d "$tree" ] && [ ! -L "$tree" ] || continue
    for base in "$tree"/*; do
      [ -d "$base" ] && [ ! -L "$base" ] || continue
      add_base "$base"
    done
  done
fi

freed_kb=0
size_of() { du -sxk -- "$1" 2>/dev/null | awk 'NR == 1 {print $1}'; }

# container_remove PATH: remove PATH through the machine's container runtime.
# The container is root because the host user already failed to remove the
# tree. Its mount names only PATH's parent, and GNU rm keeps the same
# one-file-system guard as the host attempt.
container_remove() {
  local path=$1 parent name output
  local -a docker
  parent=${path%/*}
  name=${path##*/}
  docker=(docker)
  if [ -n "${DOCKER_HOST:-}" ]; then
    command -v docker >/dev/null 2>&1 || docker=(podman)
    export DOCKER_HOST
  elif ! docker info >/dev/null 2>&1; then
    docker=(sudo -n docker)
  fi
  output=$("${docker[@]}" run --rm --network none --user 0:0 \
    -v "$parent:/dyna-ci-clean" "${DYNA_CI_CLEAN_IMAGE:-debian:12-slim}" \
    rm -rf --one-file-system -- "/dyna-ci-clean/$name" 2>&1) && return 0
  output=${output//$'\r'/ }
  output=${output//$'\n'/ }
  output=${output//$'\t'/ }
  [ -n "$output" ] || output="the container runtime could not remove it"
  printf 'DYNA_CI_CACHE_CLEAN_ERROR\t%s\t%s\n' "$path" "${output:0:300}" >&2
  return 1
}

# remove PATH: report PATH and its size, then delete it unless this is a dry run.
# A tree left by a root container can defeat the host rm. Retry that tree in a
# throwaway container, and keep both attempts' raw output out of the run log.
remove() {
  local path=$1 kb
  [ -e "$path" ] || [ -L "$path" ] || return 0
  # A caller never hands over a symlink, because a link may name a cache outside
  # the allow-list. Resolve the path first, so a typo, a linked parent or a new
  # caller can only ever lose Dyna's own data.
  safe_path "$path" || return 0
  kb=$(size_of "$path")
  case "$kb" in '' | *[!0-9]*) kb=0 ;; esac
  if [ "$dry" = 0 ]; then
    if ! rm -rf --one-file-system -- "$path" 2>/dev/null; then
      if [ -e "$path" ] || [ -L "$path" ]; then
        container_remove "$path" || return 1
      fi
    fi
  fi
  printf 'DYNA_CI_CACHE_CLEAN_ITEM %s %s\n' "$kb" "$path"
  freed_kb=$((freed_kb + kb))
}

# older PATH [AGE]: PATH itself is older than AGE minutes, on this file
# system, and is not a symlink. Cache entries use the default age.
older() {
  local age=${2:-$age_min}
  [ -n "$(find "$1" -xdev -maxdepth 0 -mmin +"$age" ! -type l -print -quit 2>/dev/null)" ]
}

# live_run DIR: the run holds a lock on DIR/.active for as long as it runs, so
# a lock nobody holds belongs to a run that died, and its directory may go.
# The lock ends with its holder: a killed engine, SSH client or scope leaves no
# lock behind, only the file.
live_run() {
  [ -f "$1/.active" ] || return 1
  ! flock -n "$1/.active" true 2>/dev/null </dev/null
}

# prune_runs BASE: the run directories that no live run holds and that are old.
prune_runs() {
  local run failed=0
  for run in "$1"/runs/*; do
    [ -d "$run" ] && [ ! -L "$run" ] || continue
    live_run "$run" && continue
    older "$run" "$run_age_min" || continue
    remove "$run" || failed=1
  done
  return "$failed"
}

# cap_kb NAME: the size cap of one cache in KiB. The defaults bound the
# caches of a machine that runs CI every day and still keep enough to stay warm.
# A zero cap, or DYNA_CI_CACHE_CAP_KB=0, turns the size bound off.
cap_kb() {
  if [ -n "$cap_override" ]; then printf '%s\n' "$cap_override"; return; fi
  case "$1" in
  go-build) echo $((20 * 1024 * 1024)) ;;
  go-mod) echo $((10 * 1024 * 1024)) ;;
  npm) echo $((5 * 1024 * 1024)) ;;
  *) echo $((5 * 1024 * 1024)) ;;
  esac
}

# make_writable PATH: give the owner write permission on every real directory
# inside PATH, so a read-only Go module directory can be removed. It changes
# directories only and follows no symlink.
make_writable() {
  [ -d "$1" ] && [ ! -L "$1" ] || return 0
  # Never chmod a tree until its resolved path proves it is Dyna's own.
  safe_path "$1" || return 1
  if ! find "$1" -xdev -type d -exec chmod u+w -- {} +; then
    echo "dyna-ci-clean: cannot make $1 writable" >&2
    return 1
  fi
}

# trim_cache NAME DIR CAP: remove the cache units that nothing has used for
# longer than the age limit, and the oldest units until DIR is at or below CAP
# KiB. A unit is the smallest part that cache can lose on its own:
#   go-build  one top-level shard or file
#   go-mod    one extracted module directory selected by its @version boundary
#             at any depth, one download cache directory, or one direct VCS
#             checkout under cache/vcs
#   npm       one hash shard four levels down
# A module root can sit two levels down (gopkg.in/yaml.v3@v3), four levels down
# (github.com/foo/bar/v2@v2) or deeper, so select the @version boundary itself
# and never descend into it. A direct or private VCS download lives in
# cache/vcs/<hash> as a bare checkout, which is one unit. Go keeps extracted
# modules read-only, so each chosen module unit is made writable before it is
# removed.
trim_cache() {
  local name=$1 dir=$2 cap=$3
  case "$name" in
  go-mod)
    trim_units "$dir" "$cap" 1 -mindepth 2 \
      \( -name '*@v*' -o \( -path "$dir/cache/vcs/*" -a ! -path "$dir/cache/vcs/*/*" \) \) \
      -type d -prune
    ;;
  npm) trim_units "$dir" "$cap" 0 -mindepth 4 -maxdepth 4 ;;
  *) trim_units "$dir" "$cap" 0 -mindepth 1 -maxdepth 1 ;;
  esac
}

# trim_units DIR CAP MAKE_WRITABLE [FIND TEST...]: walk the real units that the
# FIND TEST selects inside DIR, oldest first. A unit goes when it is older than
# the age limit or when DIR is over CAP KiB; a young unit in a cache that fits
# ends the walk, because every later unit is younger. Go refreshes the mtime of
# a hot cache directory and keeps its objects in file entries, so each unit is
# judged by its own mtime and size. MAKE_WRITABLE=1 gives the owner write
# permission on the unit first, because Go keeps module directories read-only; a
# dry run changes no permission. A symlink is never followed or removed: it may
# name a cache outside Dyna's allow-list. A failed removal stops the trim with
# an error, and so does a cache that stays over its cap after every unit went,
# because a run must not report a bound it could not reach.
trim_units() {
  local dir=$1 cap=$2 writable=$3 total kb path _ts
  shift 3
  total=$(size_of "$dir")
  case "$total" in '' | *[!0-9]*) return 0 ;; esac
  while IFS=$'\t' read -r -d '' _ts path; do
    [ -e "$path" ] && [ ! -L "$path" ] || continue
    # Validate the resolved unit before any chmod or removal.
    safe_path "$path" || continue
    if [ "$cap" -gt 0 ] && [ "$total" -gt "$cap" ]; then
      :
    elif ! older "$path"; then
      break
    fi
    kb=$(size_of "$path")
    case "$kb" in '' | *[!0-9]*) kb=0 ;; esac
    if [ "$writable" = 1 ] && [ "$dry" = 0 ]; then
      make_writable "$path" || return 1
    fi
    remove "$path" || return 1
    total=$((total - kb))
  done < <(find "$dir" "$@" ! -type l -printf '%T@\t%p\0' 2>/dev/null | sort -z -n)
  if [ "$cap" -gt 0 ] && [ "$total" -gt "$cap" ]; then
    echo "dyna-ci-clean: $dir is still ${total} KiB, over its ${cap} KiB cap, and no trimmable unit remains" >&2
    return 1
  fi
}

# trim_caches BASE: the units that are older than the age limit and the oldest
# units until each cache fits its size cap, both while holding BASE's cache lock
# exclusively. A run takes that lock shared before it mounts a cache and holds
# it until it ends, so a run that already runs makes the lock fail here and
# keeps every entry, and a run that starts now waits for these few seconds
# instead. A bounded cache also drops its oldest recent units, so a build that
# writes all day cannot fill the machine. Any trim that fails makes the cleanup
# fail, so a caller never records a partial removal as a success.
trim_caches() {
  local base=$1 name path failed=0
  exec 8>>"$base/caches.lock" || return 1
  if ! flock -n 8; then
    exec 8>&-
    return 0
  fi
  if [ -L "$base/caches" ]; then
    echo "dyna-ci-clean: refusing linked repository cache root $base/caches" >&2
    exec 8>&-
    return 1
  fi
  for path in "$base"/caches/*; do
    name=${path##*/}
    # A cache path that is a link names another cache; never trim through it.
    [ -d "$path" ] && [ ! -L "$path" ] || continue
    trim_cache "$name" "$path" "$(cap_kb "$name")" || failed=1
  done
  # Also clean caches written before the repository cache namespace existed.
  for name in go-build go-mod npm; do
    path=$base/$name
    [ -d "$path" ] && [ ! -L "$path" ] || continue
    trim_cache "$name" "$path" "$(cap_kb "$name")" || failed=1
  done
  exec 8>&-
  return "$failed"
}

# prune_tree BASE: the finished runs and the diagnostics of old runs, then the
# old cache entries, which the cache lock keeps away from a live run.
prune_tree() {
  local path failed=0
  [ -d "$1" ] && [ ! -L "$1" ] || return 0
  prune_runs "$1" || failed=1
  for path in "$1"/diagnostics/*; do
    [ -d "$path" ] && [ ! -L "$path" ] || continue
    older "$path" || continue
    remove "$path" || failed=1
  done
  trim_caches "$1" || failed=1
  return "$failed"
}

failed=0
for base in "${bases[@]}"; do
  if ! safe_base "$base"; then
    failed=1
    continue
  fi
  prune_tree "$base" || failed=1
  # The legacy tree also holds the work trees and copies of the old runner, one
  # entry per run, at its top level. A moved-aside run names itself there, and
  # its own lock protects it. Only a cleanup asked to visit this tree does this.
  [ "$base" = "$legacy" ] || continue
  for path in "$legacy"/*; do
    [ -d "$path" ] && [ ! -L "$path" ] || continue
    case "$path" in
    # Bare Git caches are not run folders. Keep them out of this sweep: its
    # run age and run-lock check do not protect a cache with active worktrees.
    "$legacy"/*.git | "$legacy/runs" | "$legacy/diagnostics" | "$legacy/go-build" | "$legacy/go-mod" | "$legacy/npm") continue ;;
    esac
    live_run "$path" && continue
    older "$path" "$run_age_min" || continue
    remove "$path" || failed=1
  done
done

if [ "$failed" = 1 ]; then
  echo "dyna-ci-clean: cleanup did not finish; see the errors above" >&2
  exit 1
fi
printf 'DYNA_CI_CACHE_CLEAN_FREED_KB=%s\n' "$freed_kb"
