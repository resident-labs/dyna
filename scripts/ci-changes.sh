#!/usr/bin/env bash
# Decides which CI suites a change needs and prints name=true|false lines.
# Main, merge-trial and manual runs need everything. A pull request runs on the
# merge commit GitHub builds from the base branch as it is now, so its own files
# are the diff from that commit's first parent. The base SHA in the event can be
# older than that parent; diffing from it would count every main change since,
# and a docs-only change would run the Go tests.
set -euo pipefail
if [ "${EVENT:-}" != "pull_request" ]; then
  for k in relay go computers web pi; do echo "$k=true"; done
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
# Without -q, grep consumes the whole list. A large subtree diff otherwise
# gives printf SIGPIPE after an early match, and pipefail reports false.
has() { printf '%s\n' "$files" | grep -E "$1" >/dev/null && echo true || echo false; }
echo "relay=$(has '^(relay|\.github)/|^scripts/ci\.sh$')"
echo "go=$(has '(\.go$|^internal/|^go\.(mod|sum)$|^desktop/|^web/|^install\.sh$|^\.github/|^scripts/ci\.sh$)')"
echo "computers=$(has '^internal/(computers|computerweb)/|^\.github/')"
# Two web tests read these backend files, so a change to either runs the web suites.
echo "web=$(has '^web/|^internal/filesvc/files\.go$|^internal/voice/realtime/testdata/page-protocol\.json$|^\.github/|^scripts/ci\.sh$')"
echo "pi=$(has '^third_party/pi/')"
