#!/usr/bin/env bash
# Fails and names every tracked Go file that gofmt would change, in the root
# module and the desktop module.
set -euo pipefail
cd "${1:-$(dirname "$0")/..}"
mapfile -d '' files < <(git ls-files -z -- '*.go')
if ((${#files[@]} == 0)); then exit 0; fi
unformatted=$(gofmt -l "${files[@]}")
if [[ -n $unformatted ]]; then
  echo "These Go files are not gofmt-formatted; run gofmt -w on them:" >&2
  echo "$unformatted" >&2
  exit 1
fi
