#!/usr/bin/env bash
# Build branch QA before starting its read-only browser context.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${DYNA_SCRATCH:?Run QA from a Dyna session}"
dyna=${DYNA_BIN:-dyna}
child= build_dir= qa_started=false
cleanup() {
  local result=$?
  trap - EXIT HUP INT TERM
  if [[ -n $child ]]; then kill -TERM -- "-$child" 2>/dev/null || :; fi
  if [[ $qa_started == true ]]; then
    if ! timeout 30s "$dyna" browser stop; then
      echo "could not stop the QA browser" >&2
      if [[ $result == 0 ]]; then result=1; fi
    fi
  fi
  if [[ -n $child ]]; then
    kill -KILL -- "-$child" 2>/dev/null || :
    wait "$child" 2>/dev/null || :
  fi
  if [[ -n $build_dir ]]; then rm -rf -- "$build_dir"; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
run() {
  local result=0
  setsid "$@" <&0 &
  child=$!
  wait "$child" || result=$?
  if [[ $result != 0 ]]; then return "$result"; fi
  child=
}


# One queue serializes admission; two persistent lock inodes bound builds.
slot_dir="${XDG_RUNTIME_DIR:-/tmp}/dyna-qa-build-$UID"
umask 077
mkdir -p "$slot_dir"
exec 8>"$slot_dir/queue"
announced=false
waiting() {
  if [[ $announced == false ]]; then
    echo "waiting for a QA build slot"
    announced=true
  fi
}
if ! flock -n 8; then
  waiting
  run flock 8
fi
while :; do
  taken=false
  for slot in 1 2; do
    exec 9>"$slot_dir/slot.$slot"
    if flock -n 9; then taken=true; break; fi
  done
  if [[ $taken == true ]]; then break; fi
  exec 9>&-
  waiting
  run sleep 0.05
done
flock -u 8
exec 8>&-

build_dir=$(mktemp -d "$DYNA_SCRATCH/qa-build.XXXXXXXX")
run nice -n 10 env GOMAXPROCS=4 npm run build --prefix web
run nice -n 10 env GOMAXPROCS=4 go build -p 4 -o "$build_dir/qa-branch" ./scripts/qa-branch.go
flock -u 9
exec 9>&-
qa_started=true
run env DYNA_QA_READY=1 "$build_dir/qa-branch" "$@"
