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
# Package suffix, shard indexes (comma-separated). A sliced package runs its
# assigned tests on each shard (see CI_SLICE in scripts/ci.sh). Use COUNT 10.
#
# The seconds are summed test times from hosted CI run 40938561845 (2026-10-29,
# all ten shards reported). Keep them current when a package grows or a new one
# is added. Each pinned shard is picked so the measured totals stay level.
# Unpinned packages are small, so spreading them over every shard keeps the
# shards level too.
pinned=(
  internal/pr/watch=0,7,8      # 21s per sliced third
  internal/subagent=1,9        # 17s per sliced half
  internal/engine=2            # 20s
  internal/notify=3            # 17s
  internal/roomsvc=4           # 16s
  internal/workflow=5          # 11s
  internal/harness/runtime=6   # 10s
  internal/hooks=6             # 10s
  internal/worktrees=5         # 10s
  internal/localci=4           # 10s
  internal/pr/view=3           # 8s
  internal/harness=1           # 7s
  internal/visual=9            # 6s
  internal/room=2              # 4s
  internal/cli/workflows=5     # 4s
  cmd/dyna=6                   # 4s
  internal/computers=0         # 4s
)
spread=(0 1 2 3 4 5 6 7 8 9)
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
