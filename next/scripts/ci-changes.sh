#!/usr/bin/env bash
# Decides which CI suites a change needs and prints name=true|false lines.
# Main and manual runs need everything. A pull request runs on the merge commit
# GitHub builds from the base branch as it is now, so its own files are the diff
# from that commit's first parent. The base SHA in the event can be older than
# that parent; diffing from it would count every main change since, and a
# docs-only change would run the Go tests.
set -euo pipefail
if [ "${EVENT:-}" != "pull_request" ]; then
  for k in relay next web next_pi; do echo "$k=true"; done
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
ci='^\.github/|^next/scripts/ci(-changes)?\.sh$'
echo "relay=$(has "^relay/|^testdata/connect/|$ci")"
# Go tests read testdata/connect and .dyna/images. The next job also checks install.sh.
echo "next=$(has "^next/|^testdata/connect/|^\.dyna/images/|^install\.sh$|$ci")"
# The width check in the web suite runs the app on dyna-webdev.
echo "web=$(has "^next/web/|^next/cmd/dyna-webdev/|$ci")"
echo "next_pi=$(has "^next/third_party/pi/|$ci")"
