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
# Package suffix, shard indexes (comma-separated). A package on three shards runs
# a third of its tests on each (see CI_SLICE in scripts/ci.sh). Use COUNT 9.
#
# The seconds are the per-package times measured in hosted CI run 40760797903
# (2026-10-28). Keep them current when a package grows or a new one is added:
# spread shards 2, 5 and 6 carry the rest, so a heavy package left unpinned lands
# wherever its position puts it and makes that shard the slow one.
pinned=(
  internal/pr/watch=0,7,8      # 24s per sliced third
  internal/subagent=1          # 30s
  internal/engine=3            # 15s
  internal/notify=4            # 23s
  internal/roomsvc=4           # 22s
  internal/harness/runtime=3   # 13s
  internal/hooks=6             # 9s
  cmd/dyna=4                   # 8s
  internal/workflow=1          # 14s
  internal/pr/view=3           # 7s
  internal/cli/workflows=2     # 9s
  internal/harness=5           # 7s
  internal/visual=3            # 26s
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
  elif [[ $pkg == "$mod/internal/visual" ]]; then
    # Keep the later spread packages on their measured shards.
    n=$((n + 1))
  fi
  for w in ${want//,/ }; do
    if ((w % count == index)); then echo "$pkg"; fi
  done
done < <(go list ./... | sort)
