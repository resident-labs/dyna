#!/bin/bash
# The runner calls this before any step of a job. A pull request controls its own
# copy of the workflow, so a fork can rewrite runs-on to reach this runner. This
# file comes from the image, so it is the check a fork cannot edit.
#
# Exiting non-zero is not enough. The runner marks the job failed and keeps going,
# so a step with if: always() would run, and so would any action's pre step, which
# is always() by default. Any failure here kills the worker instead. The runner
# reports the job failed, no step runs, and the container ends with the runner.
set -euo pipefail

# Cancelling the job signals this script, and a cancelled job still runs always().
trap '' INT TERM
# The pause lets the error reach the job's log. Nothing runs while this script does.
trap 'status=$?; [ "$status" = 0 ] || { sleep 3; kill -KILL "$PPID"; }' EXIT

refuse() {
  echo "::error::Dyna CI runner refused this job: $1"
  exit 1
}

[ "$GITHUB_REPOSITORY" = "$DYNA_CI_REPO" ] || refuse "this runner serves $DYNA_CI_REPO, not $GITHUB_REPOSITORY"
case "$GITHUB_EVENT_NAME" in
  push | workflow_dispatch) ;;
  pull_request)
    head=$(jq -r '.pull_request.head.repo.full_name // ""' "$GITHUB_EVENT_PATH")
    [ "$head" = "$GITHUB_REPOSITORY" ] || refuse "pull request from ${head:-a deleted fork}"
    ;;
  *) refuse "event $GITHUB_EVENT_NAME is not served here" ;;
esac
