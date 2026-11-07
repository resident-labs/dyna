#!/usr/bin/env bash
set -euo pipefail
script=$(cd "$(dirname "$0")" && pwd)/deploy-trial.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export TEST_ROOT="$tmp/repo" TEST_LOG="$tmp/log"
export TEST_REAL_NODE=$(command -v node)
export TEST_REFRESH="$tmp/refreshed" TEST_CLOCK="$tmp/clock.cjs" TEST_EVENTS="$tmp/events-polls"
cat > "$TEST_CLOCK" <<'JS'
if (process.env.FAKE_TIMEOUT === "1") {
  let now = 0;
  Date.now = () => (now += 5000);
}
JS
mkdir -p "$TEST_ROOT/next/web" "$tmp/tools" "$tmp/install/bin"
printf old > "$tmp/install/bin/dyna"
cat > "$tmp/tools/git" <<'SH'
#!/usr/bin/env bash
if [[ $* == *show-toplevel* ]]; then echo "$TEST_ROOT"; elif [[ $* == *HEAD:next* ]]; then echo tree123; else echo abc123; fi
SH
cat > "$tmp/tools/npm" <<'SH'
#!/usr/bin/env bash
if [[ $* == 'run build' ]]; then mkdir -p dist; echo html > dist/index.html; fi
SH
cat > "$tmp/tools/go" <<'SH'
#!/usr/bin/env bash
printf '%s' "${FAKE_BINARY:-assets/index-app.js}" > "$3"
SH
cat > "$tmp/tools/systemd-run" <<'SH'
#!/usr/bin/env bash
echo 'stage hosts' >> "$TEST_LOG"
[[ ${FAKE_UNIT_COLLISION:-0} == 0 ]] || exit 1
while (($#)); do
 if [[ $1 == --report-dir ]]; then
  report=$2
  if [[ ${FAKE_STAGE_SLOW:-0} == 1 ]]; then echo arch > "$report/pending"; else touch "$report/done"; fi
  break
 fi
 shift
done
SH
cat > "$tmp/tools/systemctl" <<'SH'
#!/usr/bin/env bash
echo "$*" >> "$TEST_LOG"
if [[ ${FAKE_STAGE_STOPPED:-0} == 1 && $* == *is-active*dyna-stage-* ]]; then exit 3; fi
if [[ $* == *InvocationID* ]]; then echo 0123456789abcdef0123456789abcdef; fi
SH
cat > "$tmp/tools/curl" <<'SH'
#!/usr/bin/env bash
echo "curl $*" >> "$TEST_LOG"
case "$*" in
 *room.list*) echo '{"rooms":[{"id":"room-1"}]}' ;;
 *session.list*)
   if [[ ${FAKE_NOT_CURRENT:-0} == 1 && -f $TEST_REFRESH ]]; then
     echo '{"sessions":[{"id":"idle","box":"box-1","boxState":"starting","computer":"stopped"}],"seq":11}'
   elif [[ ${FAKE_NO_IDLE:-0} == 1 ]]; then
     echo '{"sessions":[{"id":"busy","box":"busy-box","boxState":"ready","turn":"turn-1","computer":"stopped"}],"seq":10}'
   else
     echo '{"sessions":[{"id":"busy","box":"busy-box","boxState":"ready","turn":"turn-1","computer":"stopped"},{"id":"terminal","box":"terminal-box","boxState":"ready","computer":"running"},{"id":"idle","box":"box-1","boxState":"ready","computer":"stopped","shellJobs":1}],"seq":10}'
   fi ;;
 *session.refresh*)
   [[ $* == *'{"session":"idle"}'* && $* != *'"room"'* ]] || exit 1
   touch "$TEST_REFRESH"
   echo 0 > "$TEST_EVENTS"
   if [[ ${FAKE_REFRESH_ERROR:-0} == 1 ]]; then exit 22; fi
   if [[ ${FAKE_UNMARKED:-0} == 1 ]]; then echo '{"marked":[]}'
   else echo '{"marked":["idle"]}'; fi ;;
 *events.list*)
   polls=$(( $(<"$TEST_EVENTS") + 1 ))
   echo "$polls" > "$TEST_EVENTS"
   if [[ ${FAKE_NULL_EVENTS:-} == always || ( ${FAKE_NULL_EVENTS:-} == first && $polls == 1 ) ]]; then
     echo '{"events":null,"names":{},"next":10}'
   elif [[ ${FAKE_NO_RETURN:-0} == 1 ]]; then echo '{"events":[],"next":10}'
   else echo '{"events":[{"seq":11,"body":{"type":"box.changed","session":"idle","box":"box-1","from":"starting","to":"ready"}}],"next":11}'; fi ;;
 *) echo "${FAKE_STATUS:-200}" ;;
esac
SH
cat > "$tmp/tools/node" <<'SH'
#!/usr/bin/env bash
if [[ $1 == - ]]; then exec "$TEST_REAL_NODE" --require "$TEST_CLOCK" "$@"; fi
cat >/dev/null; echo room-1
SH
cat > "$tmp/tools/journalctl" <<'SH'
#!/usr/bin/env bash
echo "journalctl $*" >> "$TEST_LOG"
[[ $* == *'_SYSTEMD_INVOCATION_ID=0123456789abcdef0123456789abcdef'* ]] || exit 1
if [[ -f $TEST_REFRESH ]]; then
  case "${FAKE_JOURNAL:-clean}" in
    fence) printf '%s\n' 'box box-1: host namespace uncertain: namespace flock held by conmon: exit status 252' 'lifecycle step box-1: namespace fence: exit status 252' ;;
    namespace) echo 'box box-1: host namespace uncertain: conmon exit status 252' ;;
    lifecycle) echo 'lifecycle step box-1: failed to start' ;;
  esac
fi
SH
cat > "$tmp/tools/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "$tmp/tools/"*
export PATH="$tmp/tools:$PATH"
args=(--prefix "$tmp/install" --node-bin "$tmp/tools" --socket "$tmp/socket")
"$script" "${args[@]}" --stage-wait 1201 >"$tmp/out" 2>&1 && { echo 'Oversized staging wait was accepted' >&2; exit 1; }
grep -q 'stage-wait must be' "$tmp/out"
test ! -e "$TEST_LOG"
test ! -d "$TEST_ROOT/next/web/dist"
echo tree123 > "$tmp/install/deployed-next"
"$script" "${args[@]}" >"$tmp/out" 2>&1
grep -q 'No next/ change' "$tmp/out"
test ! -e "$TEST_LOG"
rm "$tmp/install/deployed-next"
FAKE_STATUS=404 "$script" "${args[@]}" >"$tmp/out" 2>&1 && { echo 'Bad HTTP status was accepted' >&2; exit 1; }
grep -q 'fix forward on main' "$tmp/out"
test "$(<"$tmp/install/failed-next")" = tree123
test "$(grep -c restart "$TEST_LOG")" = 1
test "$(ls "$tmp/install/bin")" = dyna
[[ $(head -1 "$TEST_LOG") == 'stage hosts' ]]
test -s "$(find "$tmp/install/staging" -name dyna | head -1)"
"$script" "${args[@]}" >"$tmp/out" 2>&1 && { echo 'Failed tree was accepted' >&2; exit 1; }
grep -q 'already failed' "$tmp/out"
test "$(grep -c restart "$TEST_LOG")" = 1
# Dry runs build even with markers and do not change them on failure.
echo old-tree > "$tmp/install/failed-next"
echo tree123 > "$tmp/install/deployed-next"
FAKE_BINARY=empty "$script" "${args[@]}" --dry-run >"$tmp/out" 2>&1 && { echo 'Dry run missed bad binary' >&2; exit 1; }
test "$(<"$tmp/install/failed-next")" = old-tree
"$script" "${args[@]}" --dry-run >"$tmp/out" 2>&1
grep -q 'Dry run passed' "$tmp/out"
# A capped deploy leaves its independent helper's artifact outside stage/ pruning.
rm -f "$tmp/install/failed-next" "$tmp/install/deployed-next"
find "$tmp/install/staging" -name done -delete
FAKE_STAGE_SLOW=1 "$script" "${args[@]}" --stage-wait 0 >"$tmp/out" 2>&1
grep -q 'copies continue' "$tmp/out"
grep -q 'arch' "$tmp/out"
test -s "$(find "$tmp/install/staging" -name dyna | head -1)"
test "$(grep -c restart "$TEST_LOG")" = 2
# A stopped helper cannot be described as continuing. The incomplete snapshot remains visible.
rm -f "$tmp/install/deployed-next"
FAKE_STAGE_SLOW=1 FAKE_STAGE_STOPPED=1 "$script" "${args[@]}" --stage-wait 0 >"$tmp/out" 2>&1
grep -q 'not active' "$tmp/out"
! grep -q 'copies continue' "$tmp/out"
grep -q arch "$tmp/out"
# If the independent unit cannot be started (including a unit-name collision), never install or restart.
rm -f "$tmp/install/deployed-next"
before=$(sha256sum "$tmp/install/bin/dyna")
FAKE_UNIT_COLLISION=1 "$script" "${args[@]}" --stage-wait 0 >"$tmp/out" 2>&1 && { echo 'Unit launch failure was accepted' >&2; exit 1; }
[[ $before == "$(sha256sum "$tmp/install/bin/dyna")" ]]
grep -q 'Nothing was installed' "$tmp/out"
test "$(grep -c restart "$TEST_LOG")" = 3
# Clean journal plus a ready transition succeeds, and only the idle session is refreshed.
# Its last turn's shellJobs count is historical, not a reason to treat it as busy.
rm -f "$tmp/install/failed-next" "$tmp/install/deployed-next" "$TEST_REFRESH"
"$script" "${args[@]}" >"$tmp/out" 2>&1
grep -q 'Box lifecycle smoke passed: box-1 returned ready' "$tmp/out"
test "$(<"$tmp/install/deployed-next")" = tree123
test -f "$TEST_REFRESH"
# Empty event pages are null in the live API. Wait through one, then require the ready event.
rm -f "$tmp/install/deployed-next" "$TEST_REFRESH"
FAKE_NULL_EVENTS=first "$script" "${args[@]}" >"$tmp/out" 2>&1
grep -q 'Box lifecycle smoke passed: box-1 returned ready' "$tmp/out"
test "$(<"$TEST_EVENTS")" = 2
test "$(<"$tmp/install/deployed-next")" = tree123
test ! -f "$tmp/install/failed-next"
# StopStale can leave want=stop while session.list still says ready (covered by
# TestRefreshDuringStaleStopObservesUpdate in the engine). An unmarked refresh
# must observe the update's persisted ready transition, not refresh again.
rm -f "$tmp/install/deployed-next" "$TEST_REFRESH"
: > "$TEST_LOG"
FAKE_UNMARKED=1 FAKE_NULL_EVENTS=first "$script" "${args[@]}" >"$tmp/out" 2>&1
grep -q 'refresh did not mark target; waiting for a lifecycle transition' "$tmp/out"
grep -q 'refresh response: {"marked":\[\]}' "$tmp/out"
grep -q 'Box lifecycle smoke passed: box-1 returned ready' "$tmp/out"
test "$(grep -c 'curl .*session.refresh' "$TEST_LOG")" = 1
test "$(<"$TEST_EVENTS")" = 2
test "$(<"$tmp/install/deployed-next")" = tree123
# #2161: conmon retains the namespace flock (252), lifecycle errors follow,
# and the refreshed box never returns ready. Active service and HTTP 200 are insufficient.
rm -f "$tmp/install/deployed-next" "$TEST_REFRESH"
FAKE_JOURNAL=fence FAKE_NO_RETURN=1 "$script" "${args[@]}" >"$tmp/out" 2>&1 && { echo 'Namespace fence was accepted' >&2; exit 1; }
grep -q 'host namespace uncertain.*conmon.*252' "$tmp/out"
grep -q 'lifecycle step box-1' "$tmp/out"
grep -q 'failed: box lifecycle smoke.*fix forward' "$tmp/out"
test "$(<"$tmp/install/failed-next")" = tree123
test ! -f "$tmp/install/deployed-next"
# Either journal signature suffices; no idle box and no completed step also fail.
for scenario in namespace lifecycle no-idle timeout null-timeout unmarked-timeout unmarked-fence not-current refresh-error; do
  rm -f "$tmp/install/failed-next" "$tmp/install/deployed-next" "$TEST_REFRESH"
  case "$scenario" in
    namespace) flags=(FAKE_JOURNAL=namespace); expected='host namespace uncertain' ;;
    lifecycle) flags=(FAKE_JOURNAL=lifecycle); expected='lifecycle step box-1' ;;
    no-idle) flags=(FAKE_NO_IDLE=1 FAKE_TIMEOUT=1); expected='timed out after 180 seconds' ;;
    unmarked-timeout) flags=(FAKE_UNMARKED=1 FAKE_NO_RETURN=1 FAKE_TIMEOUT=1); expected='timed out after 180 seconds' ;;
    unmarked-fence) flags=(FAKE_UNMARKED=1 FAKE_JOURNAL=fence); expected='namespace fence or lifecycle error' ;;
    not-current) flags=(FAKE_UNMARKED=1 FAKE_NOT_CURRENT=1 FAKE_TIMEOUT=1); expected='timed out after 180 seconds' ;;
    refresh-error) flags=(FAKE_REFRESH_ERROR=1); expected='Command failed: curl' ;;
    timeout) flags=(FAKE_NO_RETURN=1 FAKE_TIMEOUT=1); expected='timed out after 180 seconds' ;;
    null-timeout) flags=(FAKE_NULL_EVENTS=always FAKE_TIMEOUT=1); expected='timed out after 180 seconds' ;;
  esac
  env "${flags[@]}" "$script" "${args[@]}" >"$tmp/out" 2>&1 && { echo "$scenario was accepted" >&2; exit 1; }
  grep -q "$expected" "$tmp/out"
  test "$(<"$tmp/install/failed-next")" = tree123
  test ! -f "$tmp/install/deployed-next"
  if [[ $scenario == no-idle ]]; then test ! -f "$TEST_REFRESH"; fi
  if [[ $scenario == unmarked-timeout ]]; then
    test "$(<"$TEST_EVENTS")" -gt 1
    grep -q 'refresh did not mark target; waiting for a lifecycle transition' "$tmp/out"
    ! grep -q 'Box lifecycle smoke passed' "$tmp/out"
  fi
  if [[ $scenario == null-timeout ]]; then
    test "$(<"$TEST_EVENTS")" -gt 1
    grep -q 'failed: box lifecycle smoke.*fix forward' "$tmp/out"
  fi
done
echo 'deploy-trial tests passed'
