#!/usr/bin/env bash
# Builds next/ from this checkout's HEAD and installs it on the trial host. Run it on that host. See docs/m1-run.md.
set -euo pipefail

prefix="$HOME/.local/opt/dyna-next"
node_bin="$prefix/node/bin"
socket="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/dyna-next/owner.sock"
unit=dyna-next.service
web_url=http://127.0.0.1:7790
web_host=dyna.localhost:7790
dry_run=false
data="$HOME/.local/share/dyna-next"
host_data="$HOME/.local/share/dyna-next-host"
stage_wait=1200
while (($#)); do
  case "$1" in
    --prefix|--node-bin|--socket|--unit|--web-url|--web-host|--data|--host-data|--stage-wait)
      (($# >= 2)) || { echo "Missing value for $1" >&2; exit 2; }
      case "$1" in
        --prefix) prefix=$2 ;;
        --node-bin) node_bin=$2 ;;
        --socket) socket=$2 ;;
        --unit) unit=$2 ;;
        --web-url) web_url=$2 ;;
        --web-host) web_host=$2 ;;
        --data) data=$2 ;;
        --host-data) host_data=$2 ;;
        --stage-wait) stage_wait=$2 ;;
      esac
      shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
root=$(git rev-parse --show-toplevel)
sha=$(git -C "$root" rev-parse --short=12 HEAD)
tree=$(git -C "$root" rev-parse HEAD:next)
mkdir -p "$prefix"
exec 9>"$prefix/deploy.lock"
flock -n 9 || { echo 'Another deploy is running; skipped.'; exit 0; }
if ! "$dry_run" && [[ -f $prefix/failed-next && $(<"$prefix/failed-next") == "$tree" ]]; then
  echo "Deploy of $sha already failed; waiting for a new next/ change." >&2
  exit 1
fi
if ! "$dry_run" && [[ -f $prefix/deployed-next && $(<"$prefix/deployed-next") == "$tree" ]]; then
  echo 'No next/ change since the last deploy; skipped.'
  exit 0
fi
step=build
failure() {
  printf '%s\n' "$tree" > "$prefix/failed-next"
  if [[ $step == build || $step == 'stage hosts' ]]; then
    echo "Deploy of $sha failed: $step. Nothing was installed." >&2
  else
    echo "Deploy of $sha failed: $step. The trial keeps the new binary; fix forward on main." >&2
  fi
}
if ! "$dry_run"; then trap failure ERR; fi
stage="$prefix/stage/$sha"
bin="$prefix/bin"
export PATH="$node_bin:$PATH"
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
mkdir -p "$stage"
rm -rf "$root/next/web/dist"
(cd "$root/next/web" && npm ci && npm run build)
test -s "$root/next/web/dist/index.html"
(cd "$root/next" && go build -o "$stage/dyna" ./cmd/dyna)
strings "$stage/dyna" | grep 'assets/index-' >/dev/null
if "$dry_run"; then
  trap - ERR
  echo "Dry run passed; staged $sha without installing."
  exit 0
fi
# The independent unit owns this immutable artifact until it finishes. Never prune staging/ with stage/.
# Its ready-host snapshot is read-only, so a new binary cannot migrate the live old engine's store.
step='stage hosts'
[[ $stage_wait =~ ^(0|[1-9][0-9]{0,3})$ ]] && ((stage_wait <= 1200)) || { echo 'stage-wait must be seconds from 0 to 1200' >&2; exit 2; }
sum=$(sha256sum "$stage/dyna" | cut -d ' ' -f 1)
artifact="$prefix/staging/$sum"
mkdir -p "$artifact"
if [[ ! -f $artifact/dyna ]]; then cp "$stage/dyna" "$artifact/dyna.new"; mv "$artifact/dyna.new" "$artifact/dyna"; fi
stage_unit="dyna-stage-${sum:0:12}"
systemd-run --user --collect --unit="$stage_unit" \
  --property="StandardOutput=append:$artifact/staging.log" --property="StandardError=append:$artifact/staging.log" \
  "$artifact/dyna" stage-host-binaries --data "$data" --host-data "$host_data" \
  --runtime "$XDG_RUNTIME_DIR/$stage_unit" --report-dir "$artifact"
waited=0
while [[ ! -f $artifact/done && $waited -lt $stage_wait ]]; do sleep 1; waited=$((waited + 1)); done
if [[ ! -f $artifact/done ]]; then
  echo "Host staging reached ${stage_wait}s wait cap; copies continue in $stage_unit."
  if [[ -f $artifact/pending ]]; then echo 'Not staged:'; cat "$artifact/pending"; else echo 'Ready-host snapshot not yet available; see staging log.'; fi
fi
if [[ -f $artifact/staging.log ]]; then cat "$artifact/staging.log"; fi
echo "Host staging log: $artifact/staging.log"
step=install
cp "$stage/dyna" "$bin/dyna.new"
mv "$bin/dyna.new" "$bin/dyna"
step=restart
systemctl --user restart "$unit"
systemctl --user is-active --quiet "$unit"
step=room.list
room=$(curl --fail --silent --show-error --retry 30 --retry-delay 1 --retry-all-errors --max-time 5 --unix-socket "$socket" -H 'Content-Type: application/json' -d '{}' http://localhost/v1/room.list | node -e 'let s=""; process.stdin.on("data", c => s += c); process.stdin.on("end", () => { const id = JSON.parse(s).rooms[0]?.id; if (!id || !/^[a-zA-Z0-9_-]+$/.test(id)) process.exit(1); process.stdout.write(id); });')
step='web check'
for path in / "/r/$room"; do
  status=$(curl --silent --show-error --retry 30 --retry-delay 1 --retry-all-errors --max-time 5 -o /dev/null -w '%{http_code}' -H "Host: $web_host" "$web_url$path")
  [[ $status == 200 ]]
done
step='record deploy'
printf '%s\n' "$tree" > "$prefix/deployed-next"
rm -f "$prefix/failed-next"
find "$prefix/stage" -mindepth 1 -maxdepth 1 -type d ! -name "$sha" -exec rm -rf -- {} +
trap - ERR
echo "Deployed $sha; service and web checks passed."
