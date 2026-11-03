#!/usr/bin/env bash
# Checks in a built dyna-base image that Chromium, launched with the desktop's flags, runs uBlock Origin Lite and
# I still don't care about cookies, and that a known ad request is blocked. Needs podman:
#   scripts/check-extensions.sh [image]      image defaults to dyna-base
# It runs its own container, named dyna-ext-check-<pid>, and removes only that one. Inside the container (DYNA_INSIDE=1)
# it starts Xvnc, launches /usr/bin/chromium with the args of browserArgs in next/box/computer/process.go, and reads
# DevTools on 9222.
set -euo pipefail

if [[ -z "${DYNA_INSIDE:-}" ]]; then
  image=${1:-dyna-base}
  name=dyna-ext-check-$$
  trap 'podman rm -f "$name" >/dev/null 2>&1 || true' EXIT
  podman run --name "$name" --user node -e DYNA_INSIDE=1 -v "$(readlink -f "$0"):/check.sh:ro" --entrypoint bash "$image" /check.sh
  exit
fi

ext=${DYNA_EXT_DIR:-/opt/dyna/extensions}
profile=$(mktemp -d)
Xvnc :1 -geometry 1280x800 -depth 24 -rfbport -1 -SecurityTypes None >/dev/null 2>&1 &
sleep 2
args=(--remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 --user-data-dir="$profile"
  --disable-dev-shm-usage --no-first-run --no-default-browser-check --password-store=basic
  --hide-crash-restore-bubble --start-maximized
  --disable-features=DisableLoadExtensionCommandLineSwitch --load-extension="$ext/ubol,$ext/isdcac" about:blank)
DISPLAY=:1 chromium "${args[@]}" >/tmp/chromium.log 2>&1 &
for _ in $(seq 30); do curl -fsm2 http://127.0.0.1:9222/json >/dev/null && break; sleep 1; done

pid=$(pgrep -f '^/usr/lib/chromium/chromium --show' | head -1 || true)
[[ -n $pid ]] || { echo "Chromium is not running; its log:" >&2; cat /tmp/chromium.log >&2; exit 1; }
mapfile -t argv < <(tr '\0' '\n' </proc/"$pid"/cmdline)
echo "argv: ${argv[*]}"
[[ $(printf '%s\n' "${argv[@]}" | grep -c '^--load-extension=') == 1 ]] || { echo "want exactly one non-empty --load-extension" >&2; exit 1; }
if grep -q "Extension error" /tmp/chromium.log; then grep "Extension error" /tmp/chromium.log; exit 1; fi

node --input-type=module -e '
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
let workers = [];
for (let i = 0; i < 20 && workers.length < 2; i++) {
  workers = (await (await fetch("http://127.0.0.1:9222/json")).json()).filter((t) => t.type === "service_worker");
  await wait(500);
}
for (const w of workers) console.log("service worker:", w.url);
if (workers.length !== 2) throw new Error("want 2 extension service workers");
const page = (await (await fetch("http://127.0.0.1:9222/json")).json()).find((t) => t.type === "page");
const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((r) => (ws.onopen = r));
let id = 0;
const pending = new Map();
ws.onmessage = (m) => { const d = JSON.parse(m.data); pending.get(d.id)?.(d); };
const send = (method, params = {}) => new Promise((r) => { pending.set(++id, r); ws.send(JSON.stringify({ id, method, params })); });
const evaluate = async (expression) => (await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true })).result.result.value;
await send("Page.navigate", { url: "https://example.com/" });
await wait(3000);
// A known ad script and a known cookie-notice script, loaded by script tags, and a control that must load. Blocked
// ones fire onerror. uBlock Origin Lite turns its rulesets on a moment after it starts, so each is retried.
const load = (url) => evaluate(`new Promise((r) => { const s = document.createElement("script"); s.src = "${url}?" + Math.random(); s.onload = () => r("loaded"); s.onerror = () => r("blocked"); document.head.append(s); })`);
const urls = { ad: "https://static.criteo.net/js/ld/publishertag.js", cookies: "https://cdn.iubenda.com/cs/iubenda_cs.js", control: "https://example.com/" };
const result = {};
for (const [name, url] of Object.entries(urls)) {
  result[name] = "";
  for (let i = 0; i < 20 && result[name] !== "blocked"; i++) {
    result[name] = await load(url);
    if (name === "control" || result[name] !== "blocked") await wait(name === "control" ? 0 : 1000);
    if (name === "control") break;
  }
  console.log(name, url, result[name]);
}
if (result.ad !== "blocked" || result.cookies !== "blocked" || result.control !== "loaded") throw new Error("not blocked as expected");
process.exit(0);
'
echo OK
