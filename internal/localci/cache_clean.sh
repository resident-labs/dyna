#!/usr/bin/env bash
# Removes Dyna's own CI caches and the directories of finished runs on one CI
# machine. Dyna shares a CI machine with other work, so this script never
# touches a path outside its allow-list: /var/tmp/dyna-local-ci, its
# /var/tmp/dyna-local-ci-<uid> node trees, and /var/tmp/dyna-ci/. It never runs
# as root, stays on one file system, and follows no symlink.
#
# Inputs come from the environment, so the caller sends one shell command:
#   DYNA_CI_CLEAN_DRY=1        report what would go, delete nothing
#   DYNA_CI_CLEAN_AGE_MIN=2880 keep everything younger than this many minutes
#   DYNA_CI_CLEAN_ROOTS=...    trees to clean (space separated)
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

if [ -n "${DYNA_CI_CLEAN_ROOTS:-}" ]; then
  read -r -a roots <<< "$DYNA_CI_CLEAN_ROOTS"
else
  roots=("$legacy" "/var/tmp/dyna-local-ci-$(id -u)")
fi
[ "${#roots[@]}" -gt 0 ] || refuse "no roots to clean"

# allowed is the only guard that keeps a typo from deleting another user's data.
allowed() {
  case "$1" in
  /var/tmp/dyna-local-ci | /var/tmp/dyna-local-ci/* | /var/tmp/dyna-local-ci-?* | /var/tmp/dyna-ci/?*) return 0 ;;
  esac
  return 1
}
for root in "${roots[@]}"; do
  case "$root" in *..*) refuse "refusing path with ..: $root" ;; esac
  allowed "$root" || refuse "refusing path outside the Dyna CI allow-list: $root"
done

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

# A run keeps DIR/.active while it runs, so a live run is never removed.
# Finished runs remove their own directory; this only clears leftovers.
prune_runs() {
  local run
  for run in "$1"/runs/*; do
    [ -d "$run" ] || continue
    [ -e "$run/.active" ] && continue
    older "$run" || continue
    remove "$run"
  done
}

# prune_tree DIR: the finished run directories, the diagnostics of old runs,
# and the build cache entries nothing has used for longer than the age limit.
prune_tree() {
  local path
  prune_runs "$1"
  for path in "$1"/diagnostics/* "$1"/go-build/*; do
    [ -d "$path" ] && [ ! -L "$path" ] || continue
    older "$path" || continue
    remove "$path"
  done
}

if [ -d "$legacy" ] && [ ! -L "$legacy" ]; then
  # The old shared Go build cache is unused since runs moved to
  # /var/tmp/dyna-local-ci-<uid>, so it goes whole; the rest goes by age.
  remove "$legacy/go-build"
  prune_tree "$legacy"
  for path in "$legacy"/*; do
    [ -d "$path" ] && [ ! -L "$path" ] || continue
    case "$path" in "$legacy/go-build" | "$legacy/runs" | "$legacy/diagnostics") continue ;; esac
    [ -e "$path/.active" ] && continue
    older "$path" || continue
    remove "$path"
  done
fi

for root in "${roots[@]}"; do
  [ "$root" = "$legacy" ] && continue
  [ -d "$root" ] || continue
  for node_dir in "$root"/*; do
    [ -d "$node_dir" ] && [ ! -L "$node_dir" ] || continue
    prune_tree "$node_dir"
  done
done

printf 'DYNA_CI_CACHE_CLEAN_FREED_KB=%s\n' "$freed_kb"
