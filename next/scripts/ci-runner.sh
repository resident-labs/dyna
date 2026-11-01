#!/usr/bin/env bash
# Decides whether this run's heavy jobs use Dyna's self-hosted runners and prints
# self_hosted=true|false. The answer is false unless every condition holds, so a
# repository with no variables set keeps GitHub-hosted runners.
#
# ENABLED is the owner's kill switch. SAME_REPO is false for a fork's pull request.
# LEASE is a Unix time the pool renews while it has runners, so a dead pool sends
# new runs to GitHub-hosted runners instead of leaving them queued.
#
# This only routes jobs. A pull request can edit this file, so the check a fork
# cannot change lives in the runner image: next/box/images/ci-runner/job-started.sh.
set -euo pipefail

self_hosted=false
if [ "${ENABLED:-}" = true ] && [ "${SAME_REPO:-}" = true ]; then
  case "${LEASE:-}" in
    '' | *[!0-9]*) ;;
    *) if [ "$LEASE" -gt "$(date +%s)" ]; then self_hosted=true; fi ;;
  esac
fi
echo "self_hosted=$self_hosted"
