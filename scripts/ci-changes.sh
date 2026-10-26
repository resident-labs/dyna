#!/usr/bin/env bash
# Decides which CI suites a change needs and prints name=true|false lines.
# Main, merge-trial and manual runs need everything. A pull request runs on the
# merge commit GitHub builds from the base branch as it is now, so its own files
# are the diff from that commit's first parent. The base SHA in the event can be
# older than that parent; diffing from it would count every main change since,
# and a docs-only change would run the Go tests.
set -euo pipefail
if [ "${EVENT:-}" != "pull_request" ]; then
  for k in relay go computers web; do echo "$k=true"; done
  exit 0
fi
if git rev-parse -q --verify HEAD^2 >/dev/null; then
  files=$(git diff --name-only HEAD^1 HEAD)
elif [ -n "${BASE:-}" ]; then
  files=$(git diff --name-only "$BASE"...HEAD)
else
  # A manual run on an ordinary commit sends no base SHA.
  files=$(git diff --name-only HEAD^1 HEAD)
fi
has() { printf '%s\n' "$files" | grep -Eq "$1" && echo true || echo false; }
echo "relay=$(has '^(relay|\.github)/|^scripts/ci\.sh$')"
echo "go=$(has '(\.go$|^internal/|^go\.(mod|sum)$|^desktop/|^web/|^install\.sh$|^\.github/|^scripts/ci\.sh$)')"
echo "computers=$(has '^internal/(computers|computerweb)/|^\.github/')"
echo "web=$(has '^web/|^\.github/|^scripts/ci\.sh$')"
