#!/usr/bin/env bash
# Runs on a GitHub-hosted runner beside jobs bound for the self-hosted pool.
# GitHub lets such a job wait 24 hours for a runner, so a pool that dies after a
# run was routed to it would hold that run's checks for a day. This cancels the
# run once it has work queued and has made no progress for STALL_MINUTES, so the
# run ends red and a rerun can use GitHub-hosted runners.
set -euo pipefail

stall=$((${STALL_MINUTES:-20} * 60))
jobs_url="repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/attempts/${GITHUB_RUN_ATTEMPT:-1}/jobs?per_page=100"
started=$(date +%s)

while true; do
  # The newest time any job in this run started or finished, and how many pool jobs wait.
  state=$(gh api "$jobs_url" --jq '
    [.jobs[] | select(.name != env.GITHUB_JOB)] as $jobs
    | ([$jobs[] | select(.status == "queued" and (.labels | index("dyna")))] | length) as $queued
    | ([$jobs[] | .started_at, .completed_at | select(. != null) | fromdateiso8601] | max // 0) as $last
    | ([$jobs[] | select(.status == "in_progress" and (.labels | index("dyna")))] | length) as $running
    | "\($queued) \($running) \($last)"')
  read -r queued running last <<<"$state"
  now=$(date +%s)
  if [ "$queued" = 0 ] && [ "$running" = 0 ]; then
    # Pool jobs start only after the hosted changes job, so wait out the first minutes.
    if [ $((now - started)) -gt 300 ]; then exit 0; fi
  elif [ "$queued" != 0 ] && [ $((now - (last > started ? last : started))) -ge "$stall" ]; then
    echo "::error::$queued job(s) waited ${STALL_MINUTES:-20} minutes for a self-hosted runner with no progress. Cancelling this run. A rerun uses GitHub-hosted runners once the pool's lease expires."
    gh run cancel "$GITHUB_RUN_ID"
    exit 1
  fi
  sleep 60
done
