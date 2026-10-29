#!/usr/bin/env bash
# Prints the top-level tests of one package that one slice runs: scripts/ci-slice.sh PACKAGE INDEX COUNT.
# Tests are sorted first, so every slice sees the same list and each test lands in exactly one slice.
set -euo pipefail
cd "$(dirname "$0")/.."
pkg=${1:?usage: scripts/ci-slice.sh PACKAGE INDEX COUNT}
index=${2:?usage: scripts/ci-slice.sh PACKAGE INDEX COUNT}
count=${3:?usage: scripts/ci-slice.sh PACKAGE INDEX COUNT}
listed=$(go test -list '^Test' "$pkg")
mapfile -t names < <(grep '^Test' <<<"$listed" | sort)
for i in "${!names[@]}"; do
  if ((i % count == index)); then echo "${names[$i]}"; fi
done
