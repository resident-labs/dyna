#!/usr/bin/env bash
# Job hooks that let only CI_GO_TEST_SLOTS go-test jobs run on one host at once.
# Install this file in each runner directory, and again as
# ci-runner-slot-release.sh. Run it from the job-started hook (the name decides
# the mode) and the job-completed hook of every runner on the host.
#
# go test needs about every core of a host for a minute. Four of them together
# take four times as long and miss the CI budget. The wait happens in the "Set up
# runner" step (the job-started hook), which the budget does not count, so it
# costs the pull request nothing.
#
# A slot is a flock held by a small keeper process. The keeper lives as long as
# the runner's job process, so the slot is free however the job ends: finished,
# failed, cancelled or killed. The release hook only makes that a little sooner.
# A waiting job must not fail because of a queue: a go-test job holds its slot
# for the whole job, about 5 minutes under load, so several jobs in line wait for
# a long time. A waiter gives up after CI_SLOT_WAIT seconds (55 minutes, inside the
# job timeout of 60), so only a holder that never ends can hang a job.
set -u

if [ "${1:-}" = --protocol ]; then
  echo dyna-ci-container-slot-v1
  exit 0
fi

# Both hook and container callers enter the queue below. The keeper runs under
# flock in a separate session, so an SSH/client failure cannot release its lock.
keep_slot() {
  local worker=$1 lock=$2 owner=$3 ready=$4 poll=$5 hold=$6
  local container=$7 limit=$8 run=$9 requester=${10}
  shift 10
  local client= cancelled=false code=0 end
  exec 9>"$lock" || return 1
  flock -n 9 || return 1
  trap 'cancelled=true' HUP INT TERM
  echo "$$" > "$lock.keeper"
  echo "$owner" > "$lock.owner"

  # A killed keeper leaves this reservation behind. Even a hook-mode successor
  # must remove that container before announcing a grant. A daemon error never
  # proves absence and therefore never clears the reservation. Only this
  # protocol writes the validated, run-scoped name under the same flock.
  remove_container() {
    local name remaining
    [ -s "$lock.container" ] || return 0
    name=$(cat "$lock.container") || return 1
    while :; do
      docker rm -f "$name" 9>&- >/dev/null 2>&1 || true
      if remaining=$(docker container ls -aq --filter "name=^/$name$" 9>&-) && [ -z "$remaining" ]; then
        rm -f "$lock.container"
        return 0
      fi
      sleep "$poll"
    done
  }
  remove_container || return 1
  if ! kill -0 "$requester" 2>/dev/null || ! kill -0 "$worker" 2>/dev/null || [ "$cancelled" = true ]; then
    rm -f "$lock.keeper" "$lock.owner"
    return 1
  fi
  if [ "$container" = - ]; then
    echo "$$" > "$ready"
    end=$((SECONDS + hold))
    while kill -0 "$worker" 2>/dev/null && [ "$SECONDS" -lt "$end" ] && [ "$cancelled" = false ]; do sleep "$poll"; done
  else
    # Save the name before create. Create cannot run user code; only the keeper
    # can start it, after the reservation is visible to the next lock holder.
    kill -0 "$worker" 2>/dev/null && [ "$cancelled" = false ] && [ ! -e "$run/cancel" ] || return 1
    echo "$container" > "$lock.container" || return 1
    echo "$$" > "$ready"
    end=$((SECONDS + limit))
    run_client() {
      local stopped=false status=0
      "$@" 9>&- &
      client=$!
      while kill -0 "$client" 2>/dev/null; do
        if ! kill -0 "$worker" 2>/dev/null || [ -e "$run/cancel" ] || [ "$cancelled" = true ] || [ "$SECONDS" -ge "$end" ]; then
          stopped=true
          kill -KILL "$client" 2>/dev/null || true
          break
        fi
        sleep "$poll"
      done
      if wait "$client"; then :; else status=$?; fi
      [ "$stopped" = false ] || return 124
      return "$status"
    }
    if run_client "$@" > "$run/create.out" 2>&1; then :; else code=$?; fi
    # Image creation has its own bound. Execution gets a fresh full limit.
    end=$((SECONDS + limit))
    if [ "$code" = 0 ] && kill -0 "$worker" 2>/dev/null && [ ! -e "$run/cancel" ] && [ "$cancelled" = false ] && [ "$SECONDS" -lt "$end" ]; then
      if run_client docker start -a "$container"; then :; else code=$?; fi
    elif [ "$code" = 0 ]; then
      code=124
    fi
    cat "$run/create.out" >&2
    # Neither an exited Docker client nor the deadline says that the daemon
    # stopped the container. Keep flock until removal is confirmed.
    remove_container || return 1
    echo "$code" > "$run/result"
    if ! kill -0 "$worker" 2>/dev/null; then rm -rf "$run"; fi
  fi
  rm -f "$lock.keeper" "$lock.owner"
}

if [ "${1:-}" = --keeper ]; then
  shift
  keep_slot "$@"
  exit $?
fi

container=- limit=0
if [ "${1:-}" = --container ]; then
  [ "$#" -ge 5 ] || { echo "usage: $0 --container NAME SECONDS -- docker create ..." >&2; exit 2; }
  container=$2 limit=$3
  shift 3
  [[ "$container" =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ ]] && [[ "$limit" =~ ^[1-9][0-9]*$ ]] && [ "$1" = -- ] || exit 2
  shift
  [ "${1:-}" = docker ] && [ "${2:-}" = create ] || exit 2
  named=false previous=
  for argument in "$@"; do
    if [ "$previous" = --name ]; then
      [ "$argument" = "$container" ] || exit 2
      named=true
    fi
    previous=$argument
  done
  [ "$named" = true ] || exit 2
else
  [ "${GITHUB_JOB:-}" = test ] || exit 0
fi
dir=${CI_SLOT_DIR:-/var/tmp/dyna-ci/slots}
slots=${CI_GO_TEST_SLOTS:-1}
wait_max=${CI_SLOT_WAIT:-3300}
hold=${CI_SLOT_HOLD:-3900}
poll=${CI_SLOT_POLL:-2}
[[ "$slots" =~ ^[1-9][0-9]*$ ]] && [[ "$wait_max" =~ ^[0-9]+$ ]] || exit 2
owner=${GITHUB_WORKSPACE:-$0}
if ! mkdir -p "$dir" 2>/dev/null || [ ! -w "$dir" ]; then
  [ "$container" = - ] && exit 0
  echo "cannot access shared go-test admission directory: $dir" >&2
  exit 75
fi

case "$(basename "$0")" in
  *release*)
    for file in "$dir"/slot.*.owner; do
      if [ "$(cat "$file" 2>/dev/null)" = "$owner" ]; then
        # The keeper may have ended before the runner's completion hook.
        # Under the runner's bash -e, an already-gone keeper is still success.
        keeper_pid=$(cat "${file%.owner}.keeper" 2>/dev/null) || continue
        kill "$keeper_pid" 2>/dev/null || true
      fi
    done
    exit 0
    ;;
esac

worker=1 run=-
if [ "$container" != - ]; then
  worker=$$
  owner=$container
  run=$(mktemp -d "$dir/run.XXXXXX") || exit 75
else
  pid=$$
  while [ "$pid" -gt 1 ]; do
    if [ "$(ps -o comm= -p "$pid" 2>/dev/null)" = Runner.Worker ]; then worker=$pid; break; fi
    pid=$(ps -o ppid= -p "$pid" 2>/dev/null | tr -d ' ')
    [ -n "$pid" ] || break
  done
fi

take() {
  local lock="$dir/slot.$1" ready keeper_pid grant_end taken=false
  ready=$(mktemp "$dir/ready.XXXXXX") || return 1
  if [ "$container" = - ]; then
    setsid -w bash "$0" --keeper "$worker" "$lock" "$owner" "$ready" "$poll" "$hold" "$container" "$limit" "$run" "$$" "$@" >/dev/null 2>&1 </dev/null &
  else
    setsid -w bash "$0" --keeper "$worker" "$lock" "$owner" "$ready" "$poll" "$hold" "$container" "$limit" "$run" "$$" "${command[@]}" > "$run/output" 2>&1 </dev/null &
  fi
  keeper_pid=$!
  grant_end=$queue_end
  # A zero queue bound means one nonblocking lock attempt. Still allow its
  # asynchronous keeper to report that attempt, as the hook did before.
  if [ "$wait_max" = 0 ]; then grant_end=$((SECONDS + 30)); fi
  while kill -0 "$keeper_pid" 2>/dev/null && [ ! -s "$ready" ] && [ "$SECONDS" -lt "$grant_end" ]; do sleep 0.02; done
  if [ -s "$ready" ]; then taken=true; holder=$(cat "$ready"); fi
  # Do not kill a keeper that may be removing a stale daemon container. It must
  # retain the lock until that cleanup succeeds. The caller can safely fail.
  if [ "$taken" = false ] && kill -0 "$keeper_pid" 2>/dev/null; then
    echo "go-test admission is still cleaning up a reserved container" >&2
    rm -f "$ready"
    [ "$container" = - ] || exit 75
    exit 1
  fi
  rm -f "$ready"
  [ "$taken" = true ]
}
command=("$@")

# Waiters take turns in the order they arrived. Without this, every waiter polls
# for a free lock and a lucky one wins each time, so an unlucky job can wait past
# the limit while newer jobs pass it. A ticket is a file named by arrival time and
# the hook's pid; only the oldest ticket whose hook is still alive may try.
# Main and merge-trial runs go ahead of pull requests: merges wait on them.
class=1
case "${GITHUB_REF:-}" in refs/heads/main | refs/heads/dyna/merge-trial/*) class=0 ;; esac
ticket="$dir/wait.$class.$(date +%s%N).$$"
: > "$ticket" 2>/dev/null || ticket=
cleanup_waiter() {
  [ -z "$ticket" ] || rm -f "$ticket"
  if [ "$container" != - ] && [ -z "${holder:-}" ]; then rm -rf "$run"; fi
}
trap cleanup_waiter EXIT
if [ "$container" != - ]; then
  [ -n "$ticket" ] || { echo "cannot create admission ticket" >&2; exit 75; }
  trap 'touch "$run/cancel"; exit 130' HUP INT TERM
fi

# my_turn: no older waiter is still alive.
my_turn() {
  [ -n "$ticket" ] || return 0
  local other pid
  for other in "$dir"/wait.*; do
    [ "$other" = "$ticket" ] && return 0
    pid=${other##*.}
    if kill -0 "$pid" 2>/dev/null; then return 1; fi
    rm -f "$other" # its hook is gone
  done
  return 0
}

queued_at=$SECONDS
queue_end=$((SECONDS + wait_max))
announced=false
while :; do
  if ! kill -0 "$worker" 2>/dev/null; then
    echo "go-test job ended while queued" >&2
    [ "$container" = - ] || exit 75
    exit 1
  fi
  if my_turn; then
    for i in $(seq 1 "$slots"); do
      if take "$i"; then
        rm -f "$ticket"
        ticket=
        echo "go-test slot $i of $slots taken"
        if [ "$container" != - ]; then
          echo "go-test admission waited $((SECONDS - queued_at)) seconds"
          while [ ! -s "$run/result" ]; do
            if ! kill -0 "$holder" 2>/dev/null; then
              echo "admission keeper died; its container reservation remains" >&2
              exit 75
            fi
            sleep "$poll"
          done
          code=$(cat "$run/result")
          cat "$run/output"
          rm -rf "$run"
          exit "$code"
        fi
        exit 0
      fi
    done
  fi
  if [ "$SECONDS" -ge "$queue_end" ]; then
    echo "::error::no go-test slot became free in ${wait_max} s; another go-test job on this host holds all $slots (see $dir)" >&2
    [ "$container" = - ] || exit 75
    exit 1
  fi
  if [ "$announced" = false ]; then echo "waiting for a go-test slot"; announced=true; fi
  sleep "$poll"
done
