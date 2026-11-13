#!/usr/bin/env bash
# Shared CI commands for the v2 tree. Reports live outside the checkout.
set -euo pipefail
# Paths below are relative to the Go module root, so they stay right when next/* moves to the repository root.
cd "$(dirname "$0")/.."
suite=${1:?usage: next/scripts/ci.sh next|next-extras|next-web|race}
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
    node --test scripts/spacing-count.test.mjs
    go vet ./...
    # Correctness and unused-code checks only; style/simplification checks are outside this CI gate.
    GOBIN="$RUNNER_TEMP/dyna-lint-bin" go install honnef.co/go/tools/cmd/staticcheck@v0.8.2
    mapfile -t packages < <(go list ./... | grep -v '/third_party/')
    "$RUNNER_TEMP/dyna-lint-bin/staticcheck" -checks='SA*,U*' "${packages[@]}"
    go test -count=1 -timeout 3m ./...
    # Join the vendored management handler and engine over private RPC.
    go test -count=1 -tags=dyna_integration ./engine/model -run '^TestKimiCompletionBindsAcrossProxyNotification$'
    ;;
  next-extras)
    # The vendored CLIProxyAPI is a separate Go module. Its management test runs as its own check,
    # in parallel with next, to keep the next job under its budget.
    (cd third_party/cliproxyapi && go test -count=1 ./internal/api/handlers/management -run '^TestDyna')
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
  # The nightly race run on main. It has no time budget.
  race) go test -race -timeout 10m ./... ;;
  *) echo "Unknown CI suite: $suite" >&2; exit 2 ;;
esac
