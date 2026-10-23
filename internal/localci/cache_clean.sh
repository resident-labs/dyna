#!/usr/bin/env bash
# Removes Dyna's own CI caches and the directories of finished runs on one CI
# machine. Dyna shares a CI machine with other work, so this script never
# touches a path outside its allow-list: /var/tmp/dyna-local-ci and its
# /var/tmp/dyna-local-ci-<uid> trees. It never runs as root, stays on one file
# system, and follows no symlink.
#
# Inputs come from the environment, so the caller sends one shell command:
#   DYNA_CI_CLEAN_DRY=1        report what would go, delete nothing
#   DYNA_CI_CLEAN_AGE_MIN=2880 keep everything younger than this many minutes
#   DYNA_CI_CLEAN_ROOTS=...    exact CI directories to clean (space separated);
#                              empty cleans every directory Dyna owns
#
# A root is the directory a run keeps its caches in, holding runs/,
# diagnostics/ and go-build/. The caller passes the one it writes to, which
# names the controller's user ID, so the ID here never has to match it.
#
# It prints one DYNA_CI_CACHE_CLEAN_ITEM <kb> <path> line per path it removes,
# then DYNA_CI_CACHE_CLEAN_FREED_KB with the total, for the run log.
set -u

dry=${DYNA_CI_CLEAN_DRY:-0}
age_min=${DYNA_CI_CLEAN_AGE_MIN:-2880}
legacy=/var/tmp/dyna-local-ci

refuse() { echo "dyna-ci-clean: $*" >&2; exit 2; }

# A runner may hold a cache, but never Dyna's CI directories as root.
[ "$(id -u)" != 0 ] || refuse "refusing to run as root"
case "$dry" in 0 | 1) ;; *) refuse "DYNA_CI_CLEAN_DRY must be 0 or 1" ;; esac
[[ "$age_min" =~ ^[0-9]+$ ]] || refuse "DYNA_CI_CLEAN_AGE_MIN must be a number of minutes"

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
    [ -d "$tree" ] || continue
    for base in "$tree"/*; do
      [ -d "$base" ] && [ ! -L "$base" ] || continue
      add_base "$base"
    done
  done
fi

freed_kb=0
size_of() { du -sxk -- "$1" 2>/dev/null | awk 'NR == 1 {print $1}'; }

# remove PATH: report PATH and its size, then delete it unless this is a dry run.
remove() {
  local path=$1 real kb
  [ -e "$path" ] || [ -L "$path" ] || return 0
  # A cached path can be a symlink out of the allow-list, as the Go caches are
  # on a machine that keeps them in the home directory.
  real=$(readlink -f -- "$path" 2>/dev/null) || return 0
  if [ -z "$real" ]; then return 0; fi
  allowed "$real" || {
    echo "dyna-ci-clean: skipping $path, it resolves outside the allow-list" >&2
    return 0
  }
  kb=$(size_of "$path")
  case "$kb" in '' | *[!0-9]*) kb=0 ;; esac
  if [ "$dry" = 0 ]; then
    rm -rf --one-file-system -- "$path" || {
      echo "dyna-ci-clean: cannot remove $path" >&2
      return 1
    }
  fi
  printf 'DYNA_CI_CACHE_CLEAN_ITEM %s %s\n' "$kb" "$path"
  freed_kb=$((freed_kb + kb))
}

# older PATH: PATH itself is old, on this file system, and is not a symlink.
older() {
  [ -n "$(find "$1" -xdev -maxdepth 0 -mmin +"$age_min" ! -type l -print -quit 2>/dev/null)" ]
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
  local run
  for run in "$1"/runs/*; do
    [ -d "$run" ] && [ ! -L "$run" ] || continue
    live_run "$run" && continue
    older "$run" || continue
    remove "$run"
  done
}

# prune_tree BASE: finished runs, the diagnostics of old runs, and the build
# cache entries that nothing has used for longer than the age limit. Entries
# younger than the limit stay, so a run keeps its warm cache.
prune_tree() {
  local path
  [ -d "$1" ] && [ ! -L "$1" ] || return 0
  prune_runs "$1"
  for path in "$1"/diagnostics/* "$1"/go-build/*; do
    [ -d "$path" ] && [ ! -L "$path" ] || continue
    older "$path" || continue
    remove "$path"
  done
}

for base in "${bases[@]}"; do
  prune_tree "$base"
done

# The legacy tree also holds the work trees and copies of the old runner, one
# entry per run, at its top level. A moved-aside run names itself there.
if [ -d "$legacy" ] && [ ! -L "$legacy" ]; then
  for path in "$legacy"/*; do
    [ -d "$path" ] && [ ! -L "$path" ] || continue
    case "$path" in "$legacy/runs" | "$legacy/diagnostics" | "$legacy/go-build") continue ;; esac
    live_run "$path" && continue
    older "$path" || continue
    remove "$path"
  done
fi

printf 'DYNA_CI_CACHE_CLEAN_FREED_KB=%s\n' "$freed_kb"
