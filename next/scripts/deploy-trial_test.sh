#!/usr/bin/env bash
set -euo pipefail
script=$(cd "$(dirname "$0")" && pwd)/deploy-trial.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export TEST_ROOT="$tmp/repo" TEST_LOG="$tmp/log"
mkdir -p "$TEST_ROOT/next/web" "$TEST_ROOT/next/third_party/pi/scripts" "$tmp/tools" "$tmp/install/bin"
printf old > "$tmp/install/bin/dyna"
printf oldpi > "$tmp/install/bin/pi.tgz"
cat > "$tmp/tools/git" <<'SH'
#!/usr/bin/env bash
if [[ $* == *show-toplevel* ]]; then echo "$TEST_ROOT"; else echo abc123; fi
SH
cat > "$tmp/tools/npm" <<'SH'
#!/usr/bin/env bash
if [[ $* == 'run build' ]]; then mkdir -p dist; echo html > dist/index.html; fi
SH
cat > "$tmp/tools/go" <<'SH'
#!/usr/bin/env bash
printf '%s' "${FAKE_BINARY:-assets/index-app.js}" > "$3"
SH
cat > "$TEST_ROOT/next/third_party/pi/scripts/pi-pack.sh" <<'SH'
#!/usr/bin/env bash
echo pi > "$1"
SH
cat > "$tmp/tools/systemctl" <<'SH'
#!/usr/bin/env bash
echo "$*" >> "$TEST_LOG"
SH
cat > "$tmp/tools/curl" <<'SH'
#!/usr/bin/env bash
if [[ $* == *room.list* ]]; then echo '{"rooms":[{"id":"room-1"}]}'; else echo "${FAKE_STATUS:-200}"; fi
SH
cat > "$tmp/tools/node" <<'SH'
#!/usr/bin/env bash
cat >/dev/null; echo room-1
SH
chmod +x "$tmp/tools/"* "$TEST_ROOT/next/third_party/pi/scripts/pi-pack.sh"
export PATH="$tmp/tools:$PATH"
args=(--prefix "$tmp/install" --node-bin "$tmp/tools" --socket "$tmp/socket" --data "$tmp/data")
FAKE_BINARY=empty "$script" "${args[@]}" --dry-run >"$tmp/out" 2>&1 && { echo 'Missing assets were accepted' >&2; exit 1; }
"$script" "${args[@]}" --dry-run >"$tmp/out" 2>&1
test "$(<"$tmp/install/bin/dyna")" = old && test ! -e "$TEST_LOG"
FAKE_STATUS=404 "$script" "${args[@]}" >"$tmp/out" 2>&1 && { echo 'Bad HTTP status was accepted' >&2; exit 1; }
grep -q 'schema changed' "$tmp/out"
test "$(grep -c restart "$TEST_LOG")" = 1
test "$(<"$tmp/install/bin/dyna-prev")" = old
# A retry must not replace the last known-good pair or restart again.
"$script" "${args[@]}" >"$tmp/out" 2>&1 && { echo 'Unresolved deploy was accepted' >&2; exit 1; }
grep -q 'previous deploy is unresolved' "$tmp/out"
test "$(<"$tmp/install/bin/dyna-prev")" = old
test "$(grep -c restart "$TEST_LOG")" = 1
