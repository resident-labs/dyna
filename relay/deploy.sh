#!/usr/bin/env bash
# Deploy the Resident Labs Connect relay.
#
#   ./deploy.sh          production: https://connect.residentlabs.dev
#   ./deploy.sh test     the test relay: https://connect-test.residentlabs.dev
#
# Tokens (files, mode 0600, never printed):
#   ~/.config/dyna/connect/deploy-token   Workers Scripts Edit; Workers Routes Edit and DNS Edit on residentlabs.dev
#   ~/.config/dyna/connect/runtime-token  Cloudflare Tunnel Edit; DNS Edit on residentlabs.dev (the relay's secret)
#
# State the relay needs again later stays in ~/.config/dyna/connect/<env>/
# (mode 0700): signing-keys.json (the private signing keys; keep them for
# rotation) and bootstrap-invite (the one-time link for the first admin).
set -euo pipefail
cd "$(dirname "$0")"
env=${1:-production}
conf=${DYNA_CONNECT_CONF:-$HOME/.config/dyna/connect}
state=$conf/$env
umask 077
mkdir -p "$state"
CLOUDFLARE_API_TOKEN=$(<"$conf/deploy-token")
export CLOUDFLARE_API_TOKEN
zone=$(curl -fsS "https://api.cloudflare.com/client/v4/zones?name=residentlabs.dev" -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN")
zone_id=$(jq -r '.result[0].id' <<<"$zone")
account_id=$(jq -r '.result[0].account.id' <<<"$zone")
export CLOUDFLARE_ACCOUNT_ID=$account_id
[ -s "$state/signing-keys.json" ] || { printf '[%s]' "$(node scripts/keygen.mjs)" >"$state/signing-keys.json"; }
if [ ! -s "$state/bootstrap-invite" ]; then
  node -e 'process.stdout.write(require("crypto").randomBytes(32).toString("base64url"))' >"$state/bootstrap-invite"
fi
bootstrap_hash=$(node -e 'process.stdout.write(require("crypto").createHash("sha256").update(require("fs").readFileSync(process.argv[1],"utf8")).digest("base64url"))' "$state/bootstrap-invite")
secrets=$(mktemp)
trap 'rm -f "$secrets"' EXIT
jq -n --rawfile tok "$conf/runtime-token" --rawfile keys "$state/signing-keys.json" --arg boot "$bootstrap_hash" \
  '{CF_API_TOKEN: ($tok | rtrimstr("\n")), RELAY_SIGNING_KEYS: $keys, BOOTSTRAP_INVITE_HASH: $boot}' >"$secrets"
envflag=()
[ "$env" = production ] || envflag=(--env "$env")
npx wrangler deploy "${envflag[@]}" --secrets-file "$secrets" --var "CF_ACCOUNT_ID:$account_id" --var "CF_ZONE_ID:$zone_id"
origin=$( [ "$env" = production ] && echo https://connect.residentlabs.dev || echo "https://connect-$env.residentlabs.dev")
echo
echo "Deployed $origin"
echo "First admin: open $origin/enroll#<token in $state/bootstrap-invite> (works once, only while no account exists)."
