#!/usr/bin/env bash
# Lists or removes folders in Dyna's scratch root on one machine:
# $HOME/.cache/dyna/scratch. It never touches a path outside that root, stays on
# one file system and follows no symlink. Dyna shares the machine with other
# work, so a removal names a path relative to the root and the script refuses
# anything that does not resolve to a real entry inside it.
#
# Inputs come from the environment, so the caller sends one shell command:
#   DYNA_SCRATCH_MODE=free     print only the FREE line
#   DYNA_SCRATCH_MODE=list     print FREE, FOLDER and BUILD lines
#   DYNA_SCRATCH_MODE=remove   remove DYNA_SCRATCH_PATH, relative to the root
#
# list prints, tab separated:
#   FREE <kb>                          free space of the root's file system
#   FOLDER <newest-epoch> <kb> <name>  one folder of the root
#   BUILD <newest-epoch> <kb> <name> <path>  a build output inside that folder,
#                                      beside its project file (Cargo.toml, package.json, ...)
# The newest time is the last change of any file inside, so the caller judges
# a folder by activity and never by its name.
set -u

root=${HOME:-}/.cache/dyna/scratch
mode=${DYNA_SCRATCH_MODE:-}
if [ -z "${HOME:-}" ] || [ ! -d "$root" ] || [ -L "$root" ]; then
  [ "$mode" = list ] || [ "$mode" = free ] && exit 0
  echo "dyna-scratch: no scratch root" >&2
  exit 1
fi
real_root=$(realpath -e -- "$root") || exit 1

# newest PATH: the last change inside PATH, in whole epoch seconds.
newest() {
  find "$1" -xdev -printf '%T@\n' 2>/dev/null | sort -n | tail -1 | cut -d. -f1
}

# kb PATH: disk use in KiB.
kb() {
  du -sk -x -- "$1" 2>/dev/null | cut -f1
}

# has_marker BUILD: a build output counts only beside the project file that
# makes it one, so a source folder that happens to be named build or dist stays.
has_marker() {
  local parent=${1%/*} marker
  case "${1##*/}" in
  target) set -- Cargo.toml pom.xml ;;
  node_modules | dist) set -- package.json ;;
  build) set -- build.gradle build.gradle.kts settings.gradle settings.gradle.kts CMakeLists.txt ;;
  .gradle) set -- build.gradle build.gradle.kts settings.gradle settings.gradle.kts ;;
  *) return 1 ;;
  esac
  for marker in "$@"; do
    [ -f "$parent/$marker" ] && [ ! -L "$parent/$marker" ] && return 0
  done
  return 1
}

plain_name() {
  case "$1" in *[[:cntrl:]]*) return 1 ;; esac
}

free_line() {
  printf 'FREE\t%s\n' "$(df -Pk "$root" | awk 'NR==2 {print $4}')"
}

case "$mode" in
free) free_line ;;
list)
  free_line
  for dir in "$root"/*/; do
    dir=${dir%/}
    name=${dir##*/}
    [ -d "$dir" ] && [ ! -L "$dir" ] && plain_name "$name" || continue
    printf 'FOLDER\t%s\t%s\t%s\n' "$(newest "$dir")" "$(kb "$dir")" "$name"
    while IFS= read -r -d '' build; do
      rel=${build#"$dir"/}
      plain_name "$rel" && has_marker "$build" || continue
      printf 'BUILD\t%s\t%s\t%s\t%s\n' "$(newest "$build")" "$(kb "$build")" "$name" "$rel"
    done < <(find "$dir" -xdev -mindepth 1 -maxdepth 6 -type d \
      \( -name target -o -name node_modules -o -name build -o -name dist -o -name .gradle \) -prune -print0 2>/dev/null)
  done
  ;;
remove)
  rel=${DYNA_SCRATCH_PATH:-}
  if [ -z "$rel" ] || [[ "$rel" = /* ]] || [[ "/$rel/" == *"/../"* ]] || [[ "/$rel/" == *"/./"* ]] || ! plain_name "$rel"; then
    echo "dyna-scratch: refusing path $rel" >&2
    exit 1
  fi
  target=$root/$rel
  if [ ! -e "$target" ] && [ ! -L "$target" ]; then exit 0; fi
  # A symlink anywhere on the path would resolve elsewhere; require the
  # resolved path to equal the plain one, and a real entry inside the root.
  if [ -L "$target" ] || [ "$(realpath -e -- "$target")" != "$real_root/$rel" ]; then
    echo "dyna-scratch: refusing linked path $rel" >&2
    exit 1
  fi
  if [ -d "$target" ]; then
    find "$target" -xdev -type d -exec chmod u+w -- {} + 2>/dev/null
  fi
  rm -rf --one-file-system -- "$target"
  ;;
*)
  echo "dyna-scratch: unknown mode" >&2
  exit 1
  ;;
esac
