#!/usr/bin/env bash
# Dev only, on the trial host: serve this next/web tree against the trial engine's real data, read only (design-taste.md,
# Each UI PR). Open it through a tunnel, ssh -L 5790:127.0.0.1:5790 <host>, at the one-time link that this run writes to
# ~/.config/dyna-next/web-qa.url (0600); the view serves only the browser that opened that link.
# The first run pairs a read-scope device named Web QA through the owner socket and keeps its cookie jar in a 0600 file.
# The pairing code goes from the pair step to the redeem step on stdin only; nothing prints the code or the cookie.
set -euo pipefail
cookie=$HOME/.config/dyna-next/web-qa.cookie
trial=dyna.localhost:7790
if ! grep -qs dyna_device "$cookie"; then
  umask 077
  mkdir -p "${cookie%/*}"
  rm -f "$cookie"
  code=$("$HOME/.local/opt/dyna-next/bin/dyna" --socket "${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/dyna-next/owner.sock" device pair --scope read | cut -d' ' -f1)
  printf '{"code":"%s","name":"Web QA"}' "$code" | curl -fsS -o /dev/null -c "$cookie" -H "Host: $trial" -H "Origin: http://$trial" \
    -H "Content-Type: application/json" --data-binary @- http://127.0.0.1:7790/v1/pair
fi
key=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
(umask 077; echo "http://localhost:${PORT:-5790}/?key=$key" > "${cookie%/*}/web-qa.url")
cd "$(dirname "$0")"
DYNA_TRIAL_KEY=$key DYNA_TRIAL_COOKIE=$cookie exec npx vite --host 127.0.0.1 --port "${PORT:-5790}" --strictPort
