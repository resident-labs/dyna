#!/usr/bin/env bash
# Prints the Go packages that one CI shard tests: scripts/ci-shard.sh INDEX COUNT.
# The slowest packages (measured in CI) get fixed shards. Every other package
# goes to a spread shard by its position in the sorted list, so a new package
# is always tested by exactly one shard.
set -euo pipefail
cd "$(dirname "$0")/.."
index=${1:?usage: scripts/ci-shard.sh INDEX COUNT}
count=${2:?usage: scripts/ci-shard.sh INDEX COUNT}
mod=$(go list -m)
# Package suffix, shard indexes (comma-separated). A package on two shards runs
# half its tests on each (see CI_SLICE in scripts/ci.sh). Use COUNT 8.
pinned=(
  internal/pr/watch=0,7
  internal/subagent=1
  internal/notify=3
  internal/harness/runtime=3
  internal/roomsvc=4
  internal/harness=4
)
spread=(2 5 6)
n=0
while read -r pkg; do
  want=
  for p in "${pinned[@]}"; do
    if [[ $pkg == "$mod/${p%=*}" ]]; then want=${p#*=}; fi
  done
  if [[ -z $want ]]; then
    want=${spread[$((n % ${#spread[@]}))]}
    n=$((n + 1))
  fi
  for w in ${want//,/ }; do
    if ((w % count == index)); then echo "$pkg"; fi
  done
done < <(go list ./... | sort)
