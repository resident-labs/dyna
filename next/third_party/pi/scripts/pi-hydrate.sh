#!/usr/bin/env bash
# Hydrate Pi's generated model data from the immutable upstream catalog pin.
set -euo pipefail
repo_root=$(git rev-parse --show-toplevel)
vendor="$repo_root/next/third_party/pi"
revision=$(node -e 'const fs=require("fs");const value=JSON.parse(fs.readFileSync(process.argv[1],"utf8")).revision;if(typeof value!=="string"||!/^sha256-[0-9a-f]{64}$/.test(value))process.exit(1);process.stdout.write(value)' "$vendor/nix/model-catalog.json") || {
  echo "third_party/pi/nix/model-catalog.json has no valid SHA-256 revision" >&2
  exit 1
}
scratch=${DYNA_SCRATCH:-${TMPDIR:-/tmp}}
mkdir -p "$scratch"
tmp=$(mktemp -d "$scratch/pi-hydrate.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
catalog="$tmp/models.all.json"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https'   --max-filesize 67108864 --output "$catalog"   "https://pi.dev/api/models/revisions/$revision?types=chat,image,classifier"
printf '%s  %s\n' "${revision#sha256-}" "$catalog" | sha256sum --check -
node "$vendor/packages/ai/scripts/hydrate-model-catalog.ts" "$catalog"
