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
[[ $stage_wait =~ ^(0|[1-9][0-9]{0,3})$ ]] && ((stage_wait <= 1200)) || { echo 'stage-wait must be seconds from 0 to 1200' >&2; exit 2; }
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
  if systemctl --user is-active --quiet "$stage_unit"; then
    echo "Host staging reached ${stage_wait}s wait cap; copies continue in $stage_unit."
  else
    echo "Host staging is incomplete and $stage_unit is not active; see its log."
  fi
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
step='box lifecycle smoke'
# Scope the journal to this systemd invocation, not older processes in the unit.
invocation=$(systemctl --user show "$unit" --property=InvocationID --value)
[[ $invocation =~ ^[a-fA-F0-9]{32}$ ]]
node - "$socket" "$invocation" <<'JS'
const { execFileSync } = require("node:child_process");
const [socket, invocation] = process.argv.slice(2);
const deadline = Date.now() + 180_000;
function run(command, args) {
  const remaining = deadline - Date.now();
  if (remaining <= 0) throw new Error("Box lifecycle smoke timed out after 180 seconds");
  return execFileSync(command, args, { encoding: "utf8", timeout: Math.min(5000, remaining) });
}
function call(method, body) {
  return JSON.parse(run("curl", ["--fail", "--silent", "--show-error", "--max-time", "5",
    "--unix-socket", socket, "-H", "Content-Type: application/json", "-d", JSON.stringify(body),
    `http://localhost/v1/${method}`]));
}
function journal() {
  const log = run("journalctl", ["--user", "--no-pager", "--output=cat", `_SYSTEMD_INVOCATION_ID=${invocation}`]);
  if (/host namespace uncertain|lifecycle step /.test(log)) {
    process.stderr.write(log);
    throw new Error("Box lifecycle smoke found a namespace fence or lifecycle error");
  }
}
// A box refreshes only once its jobs end, so the session whose host job runs this deploy names itself in DEPLOY_SMOKE_SKIP.
const skip = (process.env.DEPLOY_SMOKE_SKIP || "").split(",").filter(Boolean);
let target;
while (!target) {
  let fallback;
  journal();
  for (const room of call("room.list", {}).rooms) {
    let after;
    do {
      const page = call("session.list", { room: room.id, ...(after ? { after } : {}) });
      const ready = page.sessions.filter(s => s.box && s.boxState === "ready" && !s.turn &&
        s.computer === "stopped" && !skip.includes(s.id));
      // A box refreshes only after its background jobs end. shellJobs counts the jobs left running at the last turn's
      // end and may be stale, so it ranks candidates rather than excluding them.
      const session = ready.find(s => !s.shellJobs) ?? ready[0];
      if (session && !session.shellJobs) {
        target = { room: room.id, session: session.id, box: session.box, seq: page.seq };
        break;
      }
      fallback ??= session && { room: room.id, session: session.id, box: session.box, seq: page.seq };
      after = page.next;
    } while (after);
    if (target) break;
  }
  target ??= fallback;
  if (!target) run("sleep", ["1"]);
}
console.log(`Box lifecycle smoke selected: ${JSON.stringify(target)}`);
const response = call("session.refresh", { session: target.session });
console.log(`Box lifecycle smoke refresh response: ${JSON.stringify(response)}`);
const marked = response.marked.includes(target.session);
if (marked) {
  console.log("Box lifecycle smoke accepted explicit refresh");
} else {
  // StopStale can set want=stop while the public snapshot still says ready.
  // Observe that update instead; never issue a second refresh or accept the snapshot.
  console.log("Box lifecycle smoke refresh did not mark target; waiting for a lifecycle transition");
}
// A persisted ready transition after selection plus current ready proves completion.
// An unmarked target may instead finish its stale-stop without a wake: its persisted
// ready -> stopped transition exercises the stop path, including the namespace lock.
// A still-ready pre-refresh snapshot alone is never success.
let returnedReady = false;
for (;;) {
  journal();
  const page = call("events.list", { room: target.room, afterSeq: target.seq, limit: 100,
    types: ["box.changed"] });
  let stopped = false;
  for (const event of page.events ?? []) {
    const b = event.body;
    if (b.session === target.session && b.box === target.box && b.to === "ready" && b.from !== "ready") {
      returnedReady = true;
    }
    if (!marked && b.session === target.session && b.box === target.box && b.from === "ready" && b.to === "stopped") {
      stopped = true;
    }
  }
  target.seq = page.next;
  if (stopped) {
    journal();
    console.log(`Box lifecycle smoke passed: ${target.box} stopped after unmarked refresh`);
    break;
  }
  if (returnedReady) {
    const sessions = call("session.list", { room: target.room }).sessions;
    if (sessions.some(s => s.id === target.session && s.box === target.box && s.boxState === "ready")) {
      journal();
      console.log(`Box lifecycle smoke passed: ${target.box} returned ready`);
      break;
    }
  }
  // The deadline is shared by journal reads, API calls and waiting.
  run("sleep", ["1"]);
}
JS
step='record deploy'
printf '%s\n' "$tree" > "$prefix/deployed-next"
rm -f "$prefix/failed-next"
find "$prefix/stage" -mindepth 1 -maxdepth 1 -type d ! -name "$sha" -exec rm -rf -- {} +
trap - ERR
echo "Deployed $sha; service, web and box lifecycle checks passed."
