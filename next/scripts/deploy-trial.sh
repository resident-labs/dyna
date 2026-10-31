#!/usr/bin/env bash
set -euo pipefail

prefix="$HOME/.local/opt/dyna-next"
node_bin="$prefix/node/bin"
socket="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/dyna-next/owner.sock"
unit=dyna-next.service
web_url=http://127.0.0.1:7790
web_host=dyna.localhost:7790
data="$HOME/.local/share/dyna-next"
dry_run=false
while (($#)); do
  case "$1" in
    --prefix|--node-bin|--socket|--unit|--web-url|--web-host|--data)
      (($# >= 2)) || { echo "Missing value for $1" >&2; exit 2; }
      case "$1" in
        --prefix) prefix=$2 ;;
        --node-bin) node_bin=$2 ;;
        --socket) socket=$2 ;;
        --unit) unit=$2 ;;
        --web-url) web_url=$2 ;;
        --web-host) web_host=$2 ;;
        --data) data=$2 ;;
      esac
      shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
root=$(git rev-parse --show-toplevel)
sha=$(git -C "$root" rev-parse --short=12 HEAD)
stage="$prefix/stage/$sha"
bin="$prefix/bin"
export PATH="$node_bin:$PATH"
mkdir -p "$stage"
rm -rf "$root/next/web/dist"
(cd "$root/next/web" && npm ci && npm run build)
test -s "$root/next/web/dist/index.html" || { echo 'Web build has no index.html' >&2; exit 1; }
(cd "$root/next" && go build -o "$stage/dyna" ./cmd/dyna)
"$root/next/third_party/pi/scripts/pi-pack.sh" "$stage/pi.tgz"
strings "$stage/dyna" | grep 'assets/index-' >/dev/null || { echo 'Binary has no embedded web assets' >&2; exit 1; }
if "$dry_run"; then
  echo "Dry run passed; staged $sha without installing."
  exit 0
fi

rollback() {
  printf 'Deploy failed. Do not restart the older binary if the schema changed.\n' >&2
  printf 'Stop %s, restore %s/dyna-prev and %s/pi-prev.tgz, and inspect %s/backups/pre-upgrade-v*-*.db.\n' "$unit" "$bin" "$bin" "$data" >&2
  printf 'See next/docs/m1-run.md for schema rollback steps.\n' >&2
}
trap rollback ERR
cp "$bin/dyna" "$bin/dyna-prev"
cp "$bin/pi.tgz" "$bin/pi-prev.tgz"
cp "$stage/dyna" "$bin/dyna.new"
cp "$stage/pi.tgz" "$bin/pi.new"
mv "$bin/dyna.new" "$bin/dyna"
mv "$bin/pi.new" "$bin/pi.tgz"
systemctl --user restart "$unit"
systemctl --user is-active --quiet "$unit"
room=$(curl --fail --silent --show-error --unix-socket "$socket" -H 'Content-Type: application/json' -d '{}' http://localhost/v1/room.list | node -e 'let s=""; process.stdin.on("data", c => s += c); process.stdin.on("end", () => { const id = JSON.parse(s).rooms[0]?.id; if (!id || !/^[a-zA-Z0-9_-]+$/.test(id)) process.exit(1); process.stdout.write(id); });')
for path in / "/r/$room"; do
  status=$(curl --silent --show-error -o /dev/null -w '%{http_code}' -H "Host: $web_host" "$web_url$path")
  [[ $status == 200 ]] || { echo "Web check failed for $path: HTTP $status" >&2; false; }
done
trap - ERR
echo "Deployed $sha; service and web checks passed."
