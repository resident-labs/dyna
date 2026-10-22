#!/usr/bin/env bash
# Build the Dyna desktop client without the wails CLI.
# Linux needs webkit2gtk-4.1 and gtk3; the webkit2_41 tag selects 4.1.
set -euo pipefail
cd "$(dirname "$0")"
out=${1:-build/bin/dyna-desktop}
go build -tags desktop,production,webkit2_41 -trimpath -o "$out" .
echo "$out"
