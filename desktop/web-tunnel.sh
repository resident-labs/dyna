#!/usr/bin/env bash
# Set up a Cloudflare named tunnel for the Dyna web app, once, on your own
# Cloudflare account and domain. See docs/desktop/WEB.md.
#
#   desktop/web-tunnel.sh <app hostname> <card hostname> [tunnel name]
#   desktop/web-tunnel.sh dyna.example.com dyna-cards.example.com
#
# It keeps everything in its own directory, ~/.config/dyna/cloudflared
# (DYNA_TUNNEL_DIR overrides it): its own login certificate, tunnel
# credentials and config. It never reads or writes ~/.cloudflared, so a
# tunnel you already run for other sites is not touched. It stops if a
# tunnel with the same name exists, and cloudflared stops if a DNS record
# for either hostname exists (it never overwrites one).
#
# Needs cloudflared on PATH, or CLOUDFLARED=/path/to/cloudflared. The login
# step opens a browser page on Cloudflare; pick the zone of your domain.
set -euo pipefail

app=${1:-}
cards=${2:-}
name=${3:-dyna-web}
if [ -z "$app" ] || [ -z "$cards" ] || [ "$app" = "$cards" ]; then
  echo "usage: $0 <app hostname> <card hostname> [tunnel name]" >&2
  echo "example: $0 dyna.example.com dyna-cards.example.com" >&2
  exit 2
fi
cf=${CLOUDFLARED:-cloudflared}
command -v "$cf" >/dev/null || { echo "cloudflared is not on PATH; set CLOUDFLARED" >&2; exit 1; }
dir=${DYNA_TUNNEL_DIR:-$HOME/.config/dyna/cloudflared}
mkdir -p "$dir"
chmod 700 "$dir"
cert="$dir/.cloudflared/cert.pem"
creds="$dir/$name.json"
config="$dir/config.yml"

# 1. Log in with a certificate kept in this directory only.
if [ ! -f "$cert" ]; then
  echo "Opening the Cloudflare login. Choose the zone for $app."
  HOME="$dir" "$cf" tunnel login
fi
export TUNNEL_ORIGIN_CERT="$cert"

# 2. Make a new tunnel. Stop if the name is taken.
if "$cf" tunnel list --name "$name" --output json 2>/dev/null | grep -q '"id"'; then
  echo "A tunnel named $name already exists. Pick another name (third argument), or delete it in the Cloudflare dashboard first." >&2
  exit 1
fi
"$cf" tunnel create --credentials-file "$creds" "$name"
chmod 600 "$creds"
id=$("$cf" tunnel list --name "$name" --output json | sed -n 's/.*"id": *"\([0-9a-f-]*\)".*/\1/p' | head -1)
[ -n "$id" ] || { echo "could not read the new tunnel's ID" >&2; exit 1; }

# 3. Point both hostnames at it. cloudflared refuses an existing record.
"$cf" tunnel route dns "$name" "$app"
"$cf" tunnel route dns "$name" "$cards"

# 4. Config: the app and card listeners on loopback. No httpHostHeader:
# Dyna checks the Host the browser used.
cat > "$config" <<EOF
tunnel: $id
credentials-file: $creds
ingress:
  - hostname: $app
    service: http://127.0.0.1:7457
  - hostname: $cards
    service: http://127.0.0.1:7458
  - service: http_status:404
EOF
chmod 600 "$config"

cat <<EOF

Tunnel $name ($id) is ready. Run these two, each in its own terminal or as
a service:

  $cf tunnel --config $config run
  dyna engine run --route https://$app,https://$cards

Then pair a phone: open https://$app and type the code from
dyna engine run --pair.
EOF
