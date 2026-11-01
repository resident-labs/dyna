#!/usr/bin/env bash
# Shared CI commands for the v2 tree. Reports live outside the checkout.
set -euo pipefail
# Paths below are relative to the Go module root, so they stay right when next/* moves to the repository root.
cd "$(dirname "$0")/.."
suite=${1:?usage: next/scripts/ci.sh next|next-extras|next-pi|next-web|next-web-width-build|next-web-width|race}
export RUNNER_TEMP=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
case "$suite" in
  next)
    # Format runs first and skips third_party, which keeps upstream formatting.
    mapfile -d '' files < <(git ls-files -z -- '*.go' ':(exclude)third_party')
    if ((${#files[@]})); then
      unformatted=$(gofmt -l "${files[@]}")
      if [[ -n $unformatted ]]; then
        echo "These Go files are not gofmt-formatted; fix them with gofmt -w <files>:" >&2
        echo "$unformatted" >&2
        exit 1
      fi
    fi
    go vet ./...
    go test -count=1 -timeout 3m ./...
    # Join the vendored management handler and engine over private RPC.
    go test -count=1 -tags=dyna_integration ./engine/model -run '^TestKimiCompletionBindsAcrossProxyNotification$'
    ;;
  next-extras)
    # The vendored CLIProxyAPI is a separate Go module. Its management test runs as its own check,
    # in parallel with next, to keep the next job under its budget.
    (cd third_party/cliproxyapi && go test -count=1 ./internal/api/handlers/management -run '^TestDyna')
    ;;
  next-pi)
    cd third_party/pi
    export NODE_OPTIONS=--experimental-strip-types
    npm ci --ignore-scripts
    bash scripts/pi-hydrate.sh
    npm run build:offline
    PI_OFFLINE= node --import ./packages/coding-agent/src/experimental/source-resolver.ts --input-type=module -e '
      import { ensureTool } from "./packages/coding-agent/src/utils/tools-manager.ts";
      for (const tool of ["fd", "rg"]) {
        const path = await ensureTool(tool, ({ message }) => console.log(message));
        if (!path) throw new Error(`Unable to provision ${tool}`);
      }
    '
    npm test --workspace=@earendil-works/pi-coding-agent -- --maxWorkers="${PI_TEST_WORKERS:-2}" --reporter=default --reporter=junit --outputFile="${JUNIT_REPORT_PATH:-$RUNNER_TEMP/next-pi-test.xml}"
    npm --prefix packages/ai test -- anthropic-mid-conversation-effort.test.ts
    PI_PACK_SKIP_BUILD=1 bash scripts/pi-pack.sh "$RUNNER_TEMP/next-pi-fork.tgz"
    ;;
  next-web)
    # next/web is the v2 web app, a Svelte project with its own package.json.
    # Older Node fails to load the TypeScript configs with "No Svelte configuration found".
    if (($(node -p 'process.versions.node.split(".")[0]') < 24)); then
      echo "next/web needs Node 24 or newer, but node on PATH is $(node -v). Put Node 24 first on PATH." >&2
      exit 1
    fi
    cd web
    npm ci --prefer-offline --no-audit
    npm run check
    npm test
    npm run build
    ;;
  next-web-width-build)
    : "${DYNA_WEBDEV_BIN:?Set DYNA_WEBDEV_BIN to an absolute output path}"
    # dyna-webdev embeds the built app, so the app builds first.
    (cd web && npm ci --prefer-offline --no-audit && npm run build)
    go build -p 2 -o "$DYNA_WEBDEV_BIN" ./cmd/dyna-webdev
    ;;
  next-web-width)
    : "${DYNA_WEBDEV_BIN:?Set DYNA_WEBDEV_BIN to the built binary}"
    cd web
    npm ci --prefer-offline --no-audit
    if [[ -z ${DYNA_CHROMIUM:-} ]]; then
      # Use the headless shell for the pinned Playwright version, not snap Chromium.
      managed=$(node -e 'const path = require("node:path"); const { chromium } = require("playwright-core"); const dir = path.dirname(path.dirname(chromium.executablePath())).replace(/chromium-(\d+)$/, "chromium_headless_shell-$1"); console.log(path.join(dir, "chrome-headless-shell-linux64", "chrome-headless-shell"));')
      if [[ -x "$managed" ]]; then
        DYNA_CHROMIUM=$managed
      else
        # A machine may have a different Playwright browser revision already installed.
        cache=$(dirname "$(dirname "$(dirname "$managed")")")
        for candidate in "$cache"/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell; do
          if [[ -x "$candidate" ]]; then DYNA_CHROMIUM=$candidate; break; fi
        done
        DYNA_CHROMIUM=${DYNA_CHROMIUM:-$(command -v google-chrome || command -v google-chrome-stable || true)}
      fi
    fi
    if [[ -z "$DYNA_CHROMIUM" || ! -x "$DYNA_CHROMIUM" ]]; then
      echo "No non-snap Chromium found. Install a Playwright headless shell or set DYNA_CHROMIUM to a non-snap browser." >&2
      exit 1
    fi
    export DYNA_CHROMIUM
    echo "Browser: $DYNA_CHROMIUM"
    "$DYNA_CHROMIUM" --version
    ./node_modules/.bin/tsc --strict --noEmit --skipLibCheck --module nodenext --moduleResolution nodenext --target es2022 --allowImportingTsExtensions width-check.ts
    node width-check.ts
    ;;
  # The nightly race run on main. It has no time budget.
  race) go test -race -timeout 10m ./... ;;
  *) echo "Unknown CI suite: $suite" >&2; exit 2 ;;
esac
