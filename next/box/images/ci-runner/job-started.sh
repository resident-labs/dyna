#!/bin/bash
# The runner calls this before any step of a job. A non-zero exit fails the job
# and no step runs. A pull request controls its own copy of the workflow, so a
# fork can rewrite runs-on to reach this runner. This file comes from the image,
# so it is the check a fork cannot edit.
set -euo pipefail

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
